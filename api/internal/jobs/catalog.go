package jobs

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
)

type csvCatalog map[string]Dataset

func (c csvCatalog) Lookup(id string) (Dataset, bool) { d, ok := c[id]; return d, ok }

// LoadCatalog reads datasets.csv (dataset_id,s3_key,bytes,...), locating columns by header.
func LoadCatalog(path string) (Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("catalog %s: %w", path, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("catalog %s: empty", path)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	for _, h := range []string{"dataset_id", "s3_key", "bytes"} {
		if _, ok := col[h]; !ok {
			return nil, fmt.Errorf("catalog %s: missing column %q", path, h)
		}
	}
	cat := csvCatalog{}
	for i, r := range rows[1:] {
		n, err := strconv.ParseInt(r[col["bytes"]], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("catalog %s row %d: bytes: %w", path, i+2, err)
		}
		cat[r[col["dataset_id"]]] = Dataset{Key: r[col["s3_key"]], Bytes: n}
	}
	return cat, nil
}
