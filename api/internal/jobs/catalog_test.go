package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogReadsDatasetsCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "datasets.csv")
	csv := "dataset_id,s3_key,bytes,features,geometry_type,vertex_count,attribute_count,sha256,source\n" +
		"dc,datasets/dc.geojson,23611576,58329,Polygon,1,2,abc,microsoft\n"
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := cat.Lookup("dc"); !ok || got != (Dataset{Key: "datasets/dc.geojson", Bytes: 23611576}) {
		t.Fatalf("Lookup(dc) = %+v, %v", got, ok)
	}
	if _, ok := cat.Lookup("nope"); ok {
		t.Fatal("Lookup(nope) found a dataset")
	}
}
