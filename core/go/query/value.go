package query

import (
	"encoding/base64"
	"strconv"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// NumericValue extracts a float64 from a telemetry Value for numeric kinds
// (f64, i64, bool→0/1). ok is false for text/raw/unset.
func NumericValue(v *gantryv1.Value) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch k := v.Kind.(type) {
	case *gantryv1.Value_F64:
		return k.F64, true
	case *gantryv1.Value_I64:
		return float64(k.I64), true
	case *gantryv1.Value_Flag:
		if k.Flag {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// TextValue renders any Value as a display string (used for non-numeric
// channels and for last-value raw echoes).
func TextValue(v *gantryv1.Value) string {
	if v == nil {
		return ""
	}
	switch k := v.Kind.(type) {
	case *gantryv1.Value_F64:
		return strconv.FormatFloat(k.F64, 'g', -1, 64)
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

// ValueKind maps a Value's oneof arm to its ValueKind (mirrors
// registry.InferKind without pulling the registry into the value path).
func ValueKind(v *gantryv1.Value) gantryv1.ValueKind {
	if v == nil {
		return gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED
	}
	switch v.Kind.(type) {
	case *gantryv1.Value_F64:
		return gantryv1.ValueKind_VALUE_KIND_F64
	case *gantryv1.Value_I64:
		return gantryv1.ValueKind_VALUE_KIND_I64
	case *gantryv1.Value_Flag:
		return gantryv1.ValueKind_VALUE_KIND_BOOL
	case *gantryv1.Value_Text:
		return gantryv1.ValueKind_VALUE_KIND_TEXT
	case *gantryv1.Value_Raw:
		return gantryv1.ValueKind_VALUE_KIND_RAW
	default:
		return gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED
	}
}

// KindString gives the compact JSON tag for a ValueKind ("f64", "i64", "bool",
// "text", "raw", "unspecified").
func KindString(k gantryv1.ValueKind) string {
	switch k {
	case gantryv1.ValueKind_VALUE_KIND_F64:
		return "f64"
	case gantryv1.ValueKind_VALUE_KIND_I64:
		return "i64"
	case gantryv1.ValueKind_VALUE_KIND_BOOL:
		return "bool"
	case gantryv1.ValueKind_VALUE_KIND_TEXT:
		return "text"
	case gantryv1.ValueKind_VALUE_KIND_RAW:
		return "raw"
	default:
		return "unspecified"
	}
}

// IsNumericKind reports whether a ValueKind carries a numeric value (f64, i64,
// bool). Text and raw are non-numeric.
func IsNumericKind(k gantryv1.ValueKind) bool {
	switch k {
	case gantryv1.ValueKind_VALUE_KIND_F64, gantryv1.ValueKind_VALUE_KIND_I64, gantryv1.ValueKind_VALUE_KIND_BOOL:
		return true
	default:
		return false
	}
}
