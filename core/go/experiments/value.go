package experiments

import (
	"encoding/base64"
	"fmt"
	"strconv"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// KindString returns the CSV `kind` token for a telemetry Value's oneof arm:
// "f64", "i64", "bool", "text", "raw", or "" for an unset value.
func KindString(v *gantryv1.Value) string {
	if v == nil {
		return ""
	}
	switch v.Kind.(type) {
	case *gantryv1.Value_F64:
		return "f64"
	case *gantryv1.Value_I64:
		return "i64"
	case *gantryv1.Value_Flag:
		return "bool"
	case *gantryv1.Value_Text:
		return "text"
	case *gantryv1.Value_Raw:
		return "raw"
	default:
		return ""
	}
}

// FormatValue renders a telemetry Value to its CSV `value` cell per the
// experiment.proto export contract:
//   - f64  → %g (shortest round-trippable decimal)
//   - i64  → base-10 integer
//   - bool → "true" / "false"
//   - text → the raw string (encoding/csv handles quoting/escaping)
//   - raw  → standard base64
//
// The returned string is the logical cell value; CSV quoting is the writer's job.
func FormatValue(v *gantryv1.Value) string {
	if v == nil {
		return ""
	}
	switch k := v.Kind.(type) {
	case *gantryv1.Value_F64:
		return fmt.Sprintf("%g", k.F64)
	case *gantryv1.Value_I64:
		return strconv.FormatInt(k.I64, 10)
	case *gantryv1.Value_Flag:
		return strconv.FormatBool(k.Flag)
	case *gantryv1.Value_Text:
		return k.Text
	case *gantryv1.Value_Raw:
		return base64.StdEncoding.EncodeToString(k.Raw)
	default:
		return ""
	}
}
