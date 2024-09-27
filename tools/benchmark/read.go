package benchmark

import (
	"encoding/json"
	"os"
)

func ReadBenchmark(filename string) (ps PackageSet, err error) {
	f, err := os.Open(filename)
	if err != nil {
		return
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&ps)
	return
}
