package jsrt

import (
	"one-api/common"

	"gorm.io/gorm"
)

type JSDatabase struct {
	db *gorm.DB
}

func (jsdb *JSDatabase) Query(sql string, args ...any) []map[string]any {
	if jsdb.db == nil {
		common.SysError("JS DB is nil")
		return nil
	}

	rows, err := jsdb.db.Raw(sql, args...).Rows()
	if err != nil {
		common.SysError("JS DB Query Error: " + err.Error())
		return nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		common.SysError("JS DB Columns Error: " + err.Error())
		return nil
	}

	results := make([]map[string]any, 0, 100)
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			common.SysError("JS DB Scan Error: " + err.Error())
			continue
		}

		row := make(map[string]any, len(columns))
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
	}

	return results
}

func (jsdb *JSDatabase) Exec(sql string, args ...any) map[string]any {
	if jsdb.db == nil {
		return map[string]any{
			"rowsAffected": int64(0),
			"error":        "database is nil",
		}
	}

	result := jsdb.db.Exec(sql, args...)
	return map[string]any{
		"rowsAffected": result.RowsAffected,
		"error":        result.Error,
	}
}
