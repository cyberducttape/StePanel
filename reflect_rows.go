package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// reflectRowTable builds a keyedRowTable for a flat struct (string, int,
// int64, bool, time.Time and short []string fields, the last stored as a JSON
// array) stored as one row per map key: a
// store_key primary key, one column per field named by its JSON tag, and a
// revision. Statements are generated from the struct; the table itself is
// created by literal SQL in a migration, so a field added later without a
// migration makes writes fail instead of being dropped, and
// TestReflectRowTablesMatchTheirStructs compares structs with live tables.

type reflectColumn struct {
	name  string
	index int
	kind  reflect.Kind // reflect.Struct means time.Time
}

var timeType = reflect.TypeOf(time.Time{})

func reflectColumnsOf(typ reflect.Type) ([]reflectColumn, error) {
	var columns []reflectColumn
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return nil, fmt.Errorf("%s.%s has no JSON name to use as a column", typ.Name(), field.Name)
		}
		kind := field.Type.Kind()
		switch {
		case field.Type == timeType:
			kind = reflect.Struct
		case kind == reflect.String || kind == reflect.Int || kind == reflect.Int64 || kind == reflect.Bool:
		case kind == reflect.Slice && field.Type.Elem().Kind() == reflect.String:
		default:
			return nil, fmt.Errorf("%s.%s has unsupported type %s", typ.Name(), field.Name, field.Type)
		}
		columns = append(columns, reflectColumn{name: name, index: i, kind: kind})
	}
	return columns, nil
}

// encodeReflectRow returns the column values of a struct value.
func encodeReflectRow(columns []reflectColumn, value reflect.Value) []any {
	out := make([]any, len(columns))
	for i, c := range columns {
		field := value.Field(c.index)
		switch c.kind {
		case reflect.String:
			out[i] = field.String()
		case reflect.Int, reflect.Int64:
			out[i] = field.Int()
		case reflect.Bool:
			out[i] = int64(boolInt(field.Bool()))
		case reflect.Struct:
			out[i] = formatClaimTime(field.Interface().(time.Time))
		case reflect.Slice:
			// nil and empty are the same value (both omitted when encoded).
			out[i] = ""
			if field.Len() > 0 {
				encoded, _ := json.Marshal(field.Interface()) // a []string always encodes
				out[i] = string(encoded)
			}
		}
	}
	return out
}

func decodeReflectRow(columns []reflectColumn, target reflect.Value, values []any) error {
	for i, c := range columns {
		field := target.Field(c.index)
		switch c.kind {
		case reflect.String:
			field.SetString(*values[i].(*string))
		case reflect.Int, reflect.Int64:
			field.SetInt(*values[i].(*int64))
		case reflect.Bool:
			field.SetBool(*values[i].(*int64) == 1)
		case reflect.Struct:
			text := *values[i].(*string)
			if text == "" {
				continue
			}
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return fmt.Errorf("column %s: %w", c.name, err)
			}
			field.Set(reflect.ValueOf(parsed))
		case reflect.Slice:
			text := *values[i].(*string)
			if text == "" {
				continue
			}
			list := reflect.New(field.Type())
			if err := json.Unmarshal([]byte(text), list.Interface()); err != nil {
				return fmt.Errorf("column %s: %w", c.name, err)
			}
			field.Set(list.Elem())
		}
	}
	return nil
}

func scanTargets(columns []reflectColumn) []any {
	out := make([]any, len(columns))
	for i, c := range columns {
		switch c.kind {
		case reflect.String, reflect.Struct, reflect.Slice:
			out[i] = new(string)
		default:
			out[i] = new(int64)
		}
	}
	return out
}

// columnNames returns the quoted column identifiers (some JSON names, such as
// key or type, are SQL keywords).
func columnNames(columns []reflectColumn) []string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = `"` + c.name + `"`
	}
	return names
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// newReflectRowTable panics on an unsupported struct: it runs at package
// initialization, so a bad type fails every test and the build's first run.
func newReflectRowTable[V any](label, table string) keyedRowTable[V] {
	columns, err := reflectColumnsOf(reflect.TypeOf(*new(V)))
	if err != nil {
		panic(err)
	}
	names := columnNames(columns)
	insert := `INSERT INTO ` + table + ` (store_key, ` + strings.Join(names, ", ") + `) VALUES (?, ` + placeholders(len(names)) + `) ON CONFLICT(store_key) DO NOTHING`
	assignments := make([]string, len(names))
	for i, name := range names {
		assignments[i] = name + " = ?"
	}
	update := `UPDATE ` + table + ` SET ` + strings.Join(assignments, ", ") + `, revision = revision + 1 WHERE store_key = ? AND revision = ?`
	selectAll := `SELECT store_key, revision, ` + strings.Join(names, ", ") + ` FROM ` + table
	var clone func(V) V
	for _, c := range columns {
		if c.kind == reflect.Slice {
			// Copy list fields so the persisted view never shares a backing
			// array with the live value.
			clone = func(value V) V {
				copied := reflect.ValueOf(&value).Elem()
				for _, c := range columns {
					if c.kind == reflect.Slice {
						field := copied.Field(c.index)
						if !field.IsNil() {
							fresh := reflect.MakeSlice(field.Type(), field.Len(), field.Len())
							reflect.Copy(fresh, field)
							field.Set(fresh)
						}
					}
				}
				return value
			}
			break
		}
	}
	return keyedRowTable[V]{
		name:  label,
		clone: clone,
		equal: func(a, b V) bool {
			left, right := encodeReflectRow(columns, reflect.ValueOf(a)), encodeReflectRow(columns, reflect.ValueOf(b))
			for i := range left {
				if left[i] != right[i] {
					return false
				}
			}
			return true
		},
		load: func(ctx context.Context, db *sql.DB) (map[string]V, map[string]int64, error) {
			rows, err := db.QueryContext(ctx, selectAll)
			if err != nil {
				return nil, nil, err
			}
			defer rows.Close()
			values := map[string]V{}
			revisions := map[string]int64{}
			for rows.Next() {
				var key string
				var revision int64
				targets := scanTargets(columns)
				if err := rows.Scan(append([]any{&key, &revision}, targets...)...); err != nil {
					return nil, nil, err
				}
				var value V
				if err := decodeReflectRow(columns, reflect.ValueOf(&value).Elem(), targets); err != nil {
					return nil, nil, fmt.Errorf("%s %s: %w", label, key, err)
				}
				values[key] = value
				revisions[key] = revision
			}
			return values, revisions, rows.Err()
		},
		insert: func(ctx context.Context, tx *sql.Tx, key string, value V) (bool, error) {
			return oneRowAffected(tx.ExecContext(ctx, insert, append([]any{key}, encodeReflectRow(columns, reflect.ValueOf(value))...)...))
		},
		update: func(ctx context.Context, tx *sql.Tx, key string, value V, revision int64) (bool, error) {
			return oneRowAffected(tx.ExecContext(ctx, update, append(encodeReflectRow(columns, reflect.ValueOf(value)), key, revision)...))
		},
		remove: func(ctx context.Context, tx *sql.Tx, key string, revision int64) (bool, error) {
			return oneRowAffected(tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE store_key = ? AND revision = ?`, key, revision))
		},
	}
}

// migrateBlobToReflectTable creates table with ddl and moves the named state
// blob, a JSON map of V, into it, then deletes the blob. It writes only the
// columns the table has when the migration runs, so a field added in a
// later release (with its own migration) cannot break upgrading an older
// database.
func migrateBlobToReflectTable[V any](tx *sql.Tx, blob, table, ddl string) error {
	if _, err := tx.Exec(ddl); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = ?`, blob).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s state: %w", blob, err)
	}
	var values map[string]V
	if err := json.Unmarshal(payload, &values); err != nil {
		return fmt.Errorf("decode %s state: %w", blob, err)
	}
	all, err := reflectColumnsOf(reflect.TypeOf(*new(V)))
	if err != nil {
		return err
	}
	var columns []reflectColumn
	for _, c := range all {
		exists, err := controlPlaneColumnExists(tx, table, c.name)
		if err != nil {
			return err
		}
		if exists {
			columns = append(columns, c)
		}
	}
	insert := `INSERT INTO ` + table + ` (store_key, ` + strings.Join(columnNames(columns), ", ") + `) VALUES (?, ` + placeholders(len(columns)) + `)`
	for key, value := range values {
		if _, err := tx.Exec(insert, append([]any{key}, encodeReflectRow(columns, reflect.ValueOf(value))...)...); err != nil {
			return fmt.Errorf("migrate %s %s: %w", blob, key, err)
		}
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = ?`, blob)
	return err
}
