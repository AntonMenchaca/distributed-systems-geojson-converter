package gdal

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// ConvertError is what Convert returns when ogr2ogr exits non-zero. Stderr is the child's
// whole stderr, so Permanent can match a GDAL message wherever it appears. Error shows only
// the tail, which is the log line.
type ConvertError struct {
	Format string
	Stderr []byte
	Err    error // the exec error: "exit status 1", or "signal: killed" for an OOM kill
}

func (e *ConvertError) Error() string {
	return fmt.Sprintf("gdal: ogr2ogr %s: %v: %s", e.Format, e.Err, lastBytes(e.Stderr, 2048))
}

func (e *ConvertError) Unwrap() error { return e.Err }

// permanentRule is one GDAL message that repeats on every attempt with the same input. formats
// limits the rule to some output formats; nil means every format.
type permanentRule struct {
	name    string
	re      *regexp.Regexp
	formats []string
}

// The messages were captured by running the worker's ogr2ogr command on the H.2 poison files
// (P1, P2, P4) with GDAL 3.13. Keep each pattern tied to one message, not a generic error.
var permanentRules = []permanentRule{
	// P1: the GeoJSON reader cannot parse the file, e.g. it is cut off mid-feature.
	{name: "geojson-parse", re: regexp.MustCompile(`Failed to read GeoJSON data`)},
	// P4: one GeoJSON object is over GDAL's OGR_GEOJSON_MAX_OBJ_SIZE (200 MB by default).
	{name: "geojson-object-size", re: regexp.MustCompile(`GeoJSON object too complex/large`)},
	// P2: the file mixes geometry types that a shapefile layer cannot hold. GeoPackage holds
	// them and succeeds, so this rule does not apply to it.
	{name: "shp-mixed-geometry", re: regexp.MustCompile(`Attempt to write non-\w+ \(\w+\) geometry to \w+ shapefile`), formats: []string{"shp"}},
	// P2 for FileGDB, which reports the same mixed-type problem with a different message.
	{name: "fgdb-mixed-geometry", re: regexp.MustCompile(`Unsupported geometry type`), formats: []string{"fgdb"}},
}

// Permanent reports whether err is a conversion failure that a retry cannot fix: GDAL refused
// this input, for this output format, with a message that does not change between attempts.
// Everything else is transient, including an ogr2ogr killed for memory (no GDAL message) and
// any error that is not a ConvertError. Transient failures are retried, and the DLQ catches
// them if they keep failing.
func Permanent(err error) bool {
	var ce *ConvertError
	if !errors.As(err, &ce) {
		return false
	}
	for _, r := range permanentRules {
		if (r.formats == nil || slices.Contains(r.formats, ce.Format)) && r.re.Match(ce.Stderr) {
			return true
		}
	}
	return false
}
