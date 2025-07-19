// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
)

type release struct {
	Version string
	Date    time.Time
}

func newRelease(version, dateStr string) release {
	date, err := time.Parse(time.DateOnly, dateStr)
	if err != nil {
		panic(fmt.Sprintf("Error parsing date '%s': %v", dateStr, err))
	}
	return release{Version: version, Date: date}
}

// RustVersionAt returns the rust version as of the provided date.
func RustVersionAt(t time.Time) (string, error) {
	for _, r := range releases {
		if r.Date.After(t) {
			continue
		}
		return r.Version, nil
	}
	return "", errors.New("no Rust release found")

}

// List generated with the following script:
// curl -L https://github.com/rust-lang/rust/raw/3ca41e2/RELEASES.md | rg --replace='newRelease("$1", "$2"),' -o 'Version (\S+) \(([^)]+)\)'
var releases []release = []release{
	newRelease("1.87.0", "2025-05-15"),
	newRelease("1.86.0", "2025-04-03"),
	newRelease("1.85.1", "2025-03-18"),
	newRelease("1.85.0", "2025-02-20"),
	newRelease("1.84.1", "2025-01-30"),
	newRelease("1.84.0", "2025-01-09"),
	newRelease("1.83.0", "2024-11-28"),
	newRelease("1.82.0", "2024-10-17"),
	newRelease("1.81.0", "2024-09-05"),
	newRelease("1.80.1", "2024-08-08"),
	newRelease("1.80.0", "2024-07-25"),
	newRelease("1.79.0", "2024-06-13"),
	newRelease("1.78.0", "2024-05-02"),
	newRelease("1.77.2", "2024-04-09"),
	newRelease("1.77.1", "2024-03-28"),
	newRelease("1.77.0", "2024-03-21"),
	newRelease("1.76.0", "2024-02-08"),
	newRelease("1.75.0", "2023-12-28"),
	newRelease("1.74.1", "2023-12-07"),
	newRelease("1.74.0", "2023-11-16"),
	newRelease("1.73.0", "2023-10-05"),
	newRelease("1.72.1", "2023-09-19"),
	newRelease("1.72.0", "2023-08-24"),
	newRelease("1.71.1", "2023-08-03"),
	newRelease("1.71.0", "2023-07-13"),
	newRelease("1.70.0", "2023-06-01"),
	newRelease("1.69.0", "2023-04-20"),
	newRelease("1.68.2", "2023-03-28"),
	newRelease("1.68.1", "2023-03-23"),
	newRelease("1.68.0", "2023-03-09"),
	newRelease("1.67.1", "2023-02-09"),
	newRelease("1.67.0", "2023-01-26"),
	newRelease("1.66.1", "2023-01-10"),
	newRelease("1.66.0", "2022-12-15"),
	newRelease("1.65.0", "2022-11-03"),
	newRelease("1.64.0", "2022-09-22"),
	newRelease("1.63.0", "2022-08-11"),
	newRelease("1.62.1", "2022-07-19"),
	newRelease("1.62.0", "2022-06-30"),
	newRelease("1.61.0", "2022-05-19"),
	newRelease("1.60.0", "2022-04-07"),
	newRelease("1.59.0", "2022-02-24"),
	newRelease("1.58.1", "2022-01-20"),
	newRelease("1.58.0", "2022-01-13"),
	newRelease("1.57.0", "2021-12-02"),
	newRelease("1.56.1", "2021-11-01"),
	newRelease("1.56.0", "2021-10-21"),
	newRelease("1.55.0", "2021-09-09"),
	newRelease("1.54.0", "2021-07-29"),
	newRelease("1.53.0", "2021-06-17"),
	newRelease("1.52.1", "2021-05-10"),
	newRelease("1.52.0", "2021-05-06"),
	newRelease("1.51.0", "2021-03-25"),
	newRelease("1.50.0", "2021-02-11"),
	newRelease("1.49.0", "2020-12-31"),
	newRelease("1.48.0", "2020-11-19"),
	newRelease("1.47.0", "2020-10-08"),
	newRelease("1.46.0", "2020-08-27"),
	newRelease("1.45.2", "2020-08-03"),
	newRelease("1.45.1", "2020-07-30"),
	newRelease("1.45.0", "2020-07-16"),
	newRelease("1.44.1", "2020-06-18"),
	newRelease("1.44.0", "2020-06-04"),
	newRelease("1.43.1", "2020-05-07"),
	newRelease("1.43.0", "2020-04-23"),
	newRelease("1.42.0", "2020-03-12"),
	newRelease("1.41.1", "2020-02-27"),
	newRelease("1.41.0", "2020-01-30"),
	newRelease("1.40.0", "2019-12-19"),
	newRelease("1.39.0", "2019-11-07"),
	newRelease("1.38.0", "2019-09-26"),
	newRelease("1.37.0", "2019-08-15"),
	newRelease("1.36.0", "2019-07-04"),
	newRelease("1.35.0", "2019-05-23"),
	newRelease("1.34.2", "2019-05-14"),
	newRelease("1.34.1", "2019-04-25"),
	newRelease("1.34.0", "2019-04-11"),
	newRelease("1.33.0", "2019-02-28"),
	newRelease("1.32.0", "2019-01-17"),
	newRelease("1.31.1", "2018-12-20"),
	newRelease("1.31.0", "2018-12-06"),
	newRelease("1.30.1", "2018-11-08"),
	newRelease("1.30.0", "2018-10-25"),
	newRelease("1.29.2", "2018-10-11"),
	newRelease("1.29.1", "2018-09-25"),
	newRelease("1.29.0", "2018-09-13"),
	newRelease("1.28.0", "2018-08-02"),
	newRelease("1.27.2", "2018-07-20"),
	newRelease("1.27.1", "2018-07-10"),
	newRelease("1.27.0", "2018-06-21"),
	newRelease("1.26.2", "2018-06-05"),
	newRelease("1.26.1", "2018-05-29"),
	newRelease("1.26.0", "2018-05-10"),
	newRelease("1.25.0", "2018-03-29"),
	newRelease("1.24.1", "2018-03-01"),
	newRelease("1.24.0", "2018-02-15"),
	newRelease("1.23.0", "2018-01-04"),
	newRelease("1.22.1", "2017-11-22"),
	newRelease("1.22.0", "2017-11-22"),
	newRelease("1.21.0", "2017-10-12"),
	newRelease("1.20.0", "2017-08-31"),
	newRelease("1.19.0", "2017-07-20"),
	newRelease("1.18.0", "2017-06-08"),
	newRelease("1.17.0", "2017-04-27"),
	newRelease("1.16.0", "2017-03-16"),
	newRelease("1.15.1", "2017-02-09"),
	newRelease("1.15.0", "2017-02-02"),
	newRelease("1.14.0", "2016-12-22"),
	newRelease("1.13.0", "2016-11-10"),
	newRelease("1.12.1", "2016-10-20"),
	newRelease("1.12.0", "2016-09-29"),
	newRelease("1.11.0", "2016-08-18"),
	newRelease("1.10.0", "2016-07-07"),
	newRelease("1.9.0", "2016-05-26"),
	newRelease("1.8.0", "2016-04-14"),
	newRelease("1.7.0", "2016-03-03"),
	newRelease("1.6.0", "2016-01-21"),
	newRelease("1.5.0", "2015-12-10"),
	newRelease("1.4.0", "2015-10-29"),
	newRelease("1.3.0", "2015-09-17"),
	newRelease("1.2.0", "2015-08-07"),
	newRelease("1.1.0", "2015-06-25"),
	newRelease("1.0.0", "2015-05-15"),
	newRelease("1.0.0-alpha.2", "2015-02-20"),
	newRelease("1.0.0-alpha", "2015-01-09"),
	newRelease("0.12.0", "2014-10-09"),
	newRelease("0.11.0", "2014-07-02"),
	newRelease("0.10", "2014-04-03"),
	newRelease("0.9", "2014-01-09"),
	newRelease("0.8", "2013-09-26"),
	newRelease("0.7", "2013-07-03"),
	newRelease("0.6", "2013-04-03"),
	newRelease("0.5", "2012-12-21"),
	newRelease("0.4", "2012-10-15"),
}
