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

package sqlfs

import (
	"database/sql"
	"fmt"
)

// createTable creates a table, if it doesn't exist.  If the table
// name includes the database name, e.g., "db.tbl", it creates the
// database if necessary.
func createTable(db *sql.DB, driver, table string) error {
	// HIVE and ODPS don't support AUTO_INCREMENT
	// Hive and ODPS don't support BLOB, use BINARY instead
	var stmt string
	if driver == "mysql" {
		stmt = fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (id INT, block TEXT, PRIMARY KEY (id))", table)
	} else if driver == "hive" || driver == "maxcompute" {
		stmt = fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (id INT, block STRING)", table)
	} else {
		return fmt.Errorf("createTable not supported for %s", driver)
	}
	if _, e := db.Exec(stmt); e != nil {
		return fmt.Errorf("exec:[%s] failed: %v", stmt, e)
	}

	// NOTE: a double-check of hasTable is necessary. For example,
	// MySQL doesn't allow '-' in table names; however, if there
	// is, the db.Exec wouldn't return any error.
	has, e := hasTable(db, table)
	if e != nil {
		return fmt.Errorf("createTable cannot verify the creation: %v", e)
	}
	if !has {
		return fmt.Errorf("createTable verified table not created")
	}
	return nil
}

// dropTable removes a table if it exists.  If the table name includes
// the database name, e.g., "db.tbl", it doesn't try to remove the
// database.
func dropTable(db *sql.DB, table string) error {
	stmt := fmt.Sprintf("DROP TABLE IF EXISTS %s", table)
	if _, e := db.Exec(stmt); e != nil {
		return fmt.Errorf("dropTable %s: %v", table, e)
	}
	return nil
}

// hasTable checks if a table exists.
func hasTable(db *sql.DB, table string) (bool, error) {
	if _, e := db.Exec(fmt.Sprintf("SELECT 1 FROM %s LIMIT 1", table)); e != nil {
		return false, fmt.Errorf("hasTable %s failed: %v", table, e)
	}
	return true, nil
}
