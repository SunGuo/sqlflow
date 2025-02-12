// Copyright 2019 The SQLFlow Authors. All rights reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tensorflow

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"text/template"

	pb "sqlflow.org/sqlflow/pkg/server/proto"
	"sqlflow.org/sqlflow/pkg/sql/codegen"
	"sqlflow.org/sqlflow/pkg/sql/codegen/attribute"
)

var attributeDictionary = attribute.Dictionary{
	"train.batch_size": {attribute.Int, `[default=1]
The training batch size.
range: [1,Infinity]`, attribute.IntLowerBoundChecker(1, true)},
	"train.epoch": {attribute.Int, `[default=1]
Number of epochs the training will run.
range: [1, Infinity]`, attribute.IntLowerBoundChecker(1, true)},
	"train.verbose": {attribute.Int, `[default=0]
Show verbose logs when training.
possible values: 0, 1`, attribute.IntChoicesChecker([]int{0, 1})},
	"model.*": {attribute.Unknown, `parameters defined by the model implementation, e.g. https://www.tensorflow.org/api_docs/python/tf/estimator/DNNClassifier#__init__, customized model example: https://github.com/sql-machine-learning/models/blob/develop/sqlflow_models/dnnclassifier.py#L4`,
		attribute.EmptyChecker()},
	"validation.select": {attribute.String, `[default=""]
Specify the dataset for validation.
example: "SELECT * FROM iris.train LIMIT 100"`, nil},
}

func intArrayToJSONString(ia []int) string {
	return strings.Join(strings.Split(fmt.Sprint(ia), " "), ",")
}

func generateFeatureColumnCode(fc codegen.FeatureColumn) (string, error) {
	switch c := fc.(type) {
	case *codegen.NumericColumn:
		nc := fc.(*codegen.NumericColumn)
		return fmt.Sprintf("tf.feature_column.numeric_column(\"%s\", shape=%s)",
			nc.FieldMeta.Name,
			intArrayToJSONString(nc.FieldMeta.Shape)), nil
	case *codegen.BucketColumn:
		bc := fc.(*codegen.BucketColumn)
		sourceCode, err := generateFeatureColumnCode(bc.SourceColumn)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"tf.feature_column.bucketized_column(%s, boundaries=%s)",
			sourceCode,
			intArrayToJSONString(bc.Boundaries)), nil
	case *codegen.CategoryIDColumn:
		cc := fc.(*codegen.CategoryIDColumn)
		return fmt.Sprintf("tf.feature_column.categorical_column_with_identity(key=\"%s\", num_buckets=%d)",
			cc.FieldMeta.Name, cc.BucketSize), nil
	case *codegen.SeqCategoryIDColumn:
		cc := fc.(*codegen.SeqCategoryIDColumn)
		return fmt.Sprintf("tf.feature_column.sequence_categorical_column_with_identity(key=\"%s\", num_buckets=%d)",
			cc.FieldMeta.Name, cc.BucketSize), nil
	case *codegen.CrossColumn:
		cc := fc.(*codegen.CrossColumn)
		var keysGenerated = make([]string, len(cc.Keys))
		for idx, key := range cc.Keys {
			if c, ok := key.(codegen.FeatureColumn); ok {
				code, err := generateFeatureColumnCode(c)
				if err != nil {
					return "", err
				}
				keysGenerated[idx] = code
			} else {
				return "", fmt.Errorf("field in cross column is not a FeatureColumn type: %v", key)
			}
		}
		return fmt.Sprintf(
			"tf.feature_column.crossed_column([%s], hash_bucket_size=%d)",
			strings.Join(keysGenerated, ","), cc.HashBucketSize), nil
	case *codegen.EmbeddingColumn:
		ec := fc.(*codegen.EmbeddingColumn)
		catColumn, ok := ec.CategoryColumn.(codegen.FeatureColumn)
		if !ok {
			return "", fmt.Errorf("embedding generate code error, input is not featureColumn: %s", ec.CategoryColumn)
		}
		sourceCode, err := generateFeatureColumnCode(catColumn)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("tf.feature_column.embedding_column(%s, dimension=%d, combiner=\"%s\")",
			sourceCode, ec.Dimension, ec.Combiner), nil
	default:
		return "", fmt.Errorf("unsupported feature column type %T on %v", c, c)
	}
}

func attrToPythonValue(attr interface{}) string {
	switch attr.(type) {
	case int:
		return fmt.Sprintf("%d", attr.(int))
	case int64:
		return fmt.Sprintf("%d", attr.(int64))
	case float32:
		return fmt.Sprintf("%f", attr.(float32))
	case float64: // FIXME(typhoonzero): may never use
		return fmt.Sprintf("%f", attr.(float64))
	case []int:
		return intArrayToJSONString(attr.([]int))
		// TODO(typhoonzero): support []float etc.
	case []interface{}:
		tmplist := attr.([]interface{})
		if len(tmplist) > 0 {
			if _, ok := tmplist[0].(int); ok {
				intlist := []int{}
				for _, v := range tmplist {
					intlist = append(intlist, v.(int))
				}
				return intArrayToJSONString(intlist)
			}
		}
		// TODO(typhoonzero): support []float etc.
		return "[]"
	case string:
		return attr.(string)
	default:
		return ""
	}
}

func dtypeToString(dt codegen.FieldType) string {
	switch dt {
	case codegen.Float:
		return "float32"
	case codegen.Int:
		return "int64"
	case codegen.String:
		return "string"
	default:
		return ""
	}
}

// IsKerasModel returns whether an estimator is from sqlflow_models and its qualified name
func IsKerasModel(estimator string) (bool, string) {
	if strings.HasPrefix(estimator, "sqlflow_models.") {
		return true, estimator
	}
	return false, fmt.Sprintf("tf.estimator.%s", estimator)
}

// Train generates a Python program for train a TensorFlow model.
func Train(ir *codegen.TrainIR) (string, error) {
	if err := attributeDictionary.Validate(ir.Attributes); err != nil {
		return "", err
	}
	trainParams := make(map[string]interface{})
	modelParams := make(map[string]interface{})
	for attrKey, attr := range ir.Attributes {
		if strings.HasPrefix(attrKey, "train.") {
			trainParams[strings.Replace(attrKey, "train.", "", 1)] = attr
		}
		if strings.HasPrefix(attrKey, "model.") {
			modelParams[strings.Replace(attrKey, "model.", "", 1)] = attr
		}
	}
	// Add default params for batch_size, epoch, verbose
	// TODO(typhoonzero): use feature definition dictionary.
	if _, ok := trainParams["batch_size"]; !ok {
		trainParams["batch_size"] = 1
	}
	if _, ok := trainParams["epoch"]; !ok {
		trainParams["epoch"] = 1
	}
	if _, ok := trainParams["verbose"]; !ok {
		trainParams["verbose"] = 0
	}

	featureColumnsCode := []string{}
	perTargetFeatureColumnsCode := []string{}
	fieldMetas := []*codegen.FieldMeta{}
	for target, fcList := range ir.Features {
		for _, fc := range fcList {
			fcCode, err := generateFeatureColumnCode(fc)
			if err != nil {
				return "", err
			}
			perTargetFeatureColumnsCode = append(perTargetFeatureColumnsCode, fcCode)
			if len(fc.GetFieldMeta()) > 0 {
				for _, fm := range fc.GetFieldMeta() {
					fieldMetas = append(fieldMetas, fm)
				}
			}
		}
		featureColumnsCode = append(featureColumnsCode,
			fmt.Sprintf("\"%s\": [%s]", target, strings.Join(perTargetFeatureColumnsCode, ",\n")))
	}
	isKeras, estimatorStr := IsKerasModel(ir.Estimator)

	filler := trainFiller{
		DataSource:        ir.DataSource,
		TrainSelect:       ir.Select,
		ValidationSelect:  ir.ValidationSelect,
		Estimator:         estimatorStr,
		IsKerasModel:      isKeras,
		FieldMetas:        fieldMetas,
		FeatureColumnCode: fmt.Sprintf("{%s}", strings.Join(featureColumnsCode, ",\n")),
		Y:                 ir.Label.GetFieldMeta()[0], // TODO(typhoonzero): label only support numericColumn.
		ModelParams:       modelParams,
		TrainParams:       trainParams,
		Save:              "model_save", // TODO(typhoonzero): executor.go will save the working directory, should test later.

	}
	var program bytes.Buffer
	var trainTemplate = template.Must(template.New("Train").Funcs(template.FuncMap{
		"intArrayToJSONString": intArrayToJSONString,
		"attrToPythonValue":    attrToPythonValue,
		"dtypeToString":        dtypeToString,
	}).Parse(tfTrainTemplateText))
	if err := trainTemplate.Execute(&program, filler); err != nil {
		return "", err
	}

	return program.String(), nil
}

// Pred generates a Python program for predict using a TensorFlow model.
func Pred(ir *codegen.PredictIR, session *pb.Session) (string, error) {
	modelParams := make(map[string]interface{})
	for attrKey, attr := range ir.TrainIR.Attributes {
		if strings.HasPrefix(attrKey, "model.") {
			modelParams[strings.Replace(attrKey, "model.", "", 1)] = attr
		}
	}
	featureColumnsCode := []string{}
	perTargetFeatureColumnsCode := []string{}
	fieldMetas := []*codegen.FieldMeta{}
	for target, fcList := range ir.TrainIR.Features {
		for _, fc := range fcList {
			fcCode, err := generateFeatureColumnCode(fc)
			if err != nil {
				return "", err
			}
			perTargetFeatureColumnsCode = append(perTargetFeatureColumnsCode, fcCode)
			if len(fc.GetFieldMeta()) > 0 {
				for _, fm := range fc.GetFieldMeta() {
					fieldMetas = append(fieldMetas, fm)
				}
			}
		}
		featureColumnsCode = append(featureColumnsCode,
			fmt.Sprintf("\"%s\": [%s]", target, strings.Join(perTargetFeatureColumnsCode, ",\n")))
	}
	isKeras, estimatorStr := IsKerasModel(ir.TrainIR.Estimator)
	labelFM := ir.TrainIR.Label.GetFieldMeta()[0]
	if labelFM.Name == "" {
		log.Printf("clustering model, got result table: %s, result column: %s", ir.ResultTable, ir.ResultColumn)
		// no label in train SQL means a clustering model, generate a fieldmeta using result table's column
		labelFM = &codegen.FieldMeta{
			Name:  ir.ResultColumn,
			Shape: []int{1},
			DType: codegen.Int,
		}
	}

	filler := predFiller{
		DataSource:        ir.DataSource,
		Select:            ir.Select,
		ResultTable:       ir.ResultTable,
		Estimator:         estimatorStr,
		IsKerasModel:      isKeras,
		FieldMetas:        fieldMetas,
		FeatureColumnCode: fmt.Sprintf("{%s}", strings.Join(featureColumnsCode, ",\n")),
		Y:                 labelFM,
		ModelParams:       modelParams,
		Save:              "model_save",
		HDFSNameNodeAddr:  session.HdfsNamenodeAddr,
		HiveLocation:      session.HiveLocation,
		HDFSUser:          session.HdfsUser,
		HDFSPass:          session.HdfsPass,
	}
	var program bytes.Buffer
	var predTemplate = template.Must(template.New("Pred").Funcs(template.FuncMap{
		"intArrayToJSONString": intArrayToJSONString,
		"attrToPythonValue":    attrToPythonValue,
		"dtypeToString":        dtypeToString,
	}).Parse(tfPredTemplateText))
	if err := predTemplate.Execute(&program, filler); err != nil {
		return "", err
	}

	return program.String(), nil
}
