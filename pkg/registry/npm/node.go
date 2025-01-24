package npm

import (
	"time"

	"github.com/google/oss-rebuild/internal/semver"
	"github.com/pkg/errors"
)

type NodeRelease struct {
	Version semver.Semver
	Date    time.Time
	HasMUSL bool
}

func newNodeRelease(version, dateStr string, hasMUSL bool) NodeRelease {
	sv, err := semver.New(version)
	if err != nil {
		panic(errors.Wrapf(err, "parsing version '%s'", version))
	}
	date, err := time.Parse(time.DateOnly, dateStr)
	if err != nil {
		panic(errors.Wrapf(err, "parsing date '%s'", dateStr))
	}
	return NodeRelease{Version: sv, Date: date, HasMUSL: hasMUSL}
}

// Generated using:
// curl -L https://unofficial-builds.nodejs.org/download/NodeRelease/index.json | jq -r '.[] | "newNodeRelease(\"" + .version[1:] + "\", \"" + .date + "\", " + ((.files | any(contains("linux-x64-musl"))) | tostring) + "),"'
var UnofficialNodeReleases = []NodeRelease{
	newNodeRelease("23.6.1", "2025-01-21", true),
	newNodeRelease("23.6.0", "2025-01-07", true),
	newNodeRelease("23.5.0", "2024-12-19", true),
	newNodeRelease("23.4.0", "2024-12-10", true),
	newNodeRelease("23.3.0", "2024-11-20", true),
	newNodeRelease("23.2.0", "2024-11-15", true),
	newNodeRelease("23.1.0", "2024-10-24", true),
	newNodeRelease("23.0.0", "2024-10-16", true),
	newNodeRelease("22.13.1", "2025-01-21", true),
	newNodeRelease("22.13.0", "2025-01-07", true),
	newNodeRelease("22.12.0", "2024-12-03", true),
	newNodeRelease("22.11.0", "2024-10-29", true),
	newNodeRelease("22.10.0", "2024-10-16", true),
	newNodeRelease("22.9.0", "2024-09-17", true),
	newNodeRelease("22.8.0", "2024-09-03", true),
	newNodeRelease("22.7.0", "2024-08-22", true),
	newNodeRelease("22.6.0", "2024-08-06", true),
	newNodeRelease("22.5.1", "2024-07-19", true),
	newNodeRelease("22.5.0", "2024-07-18", true),
	newNodeRelease("22.4.1", "2024-07-08", true),
	newNodeRelease("22.4.0", "2024-07-02", true),
	newNodeRelease("22.3.0", "2024-06-11", true),
	newNodeRelease("22.2.0", "2024-05-16", true),
	newNodeRelease("22.1.0", "2024-05-02", true),
	newNodeRelease("22.0.0", "2024-04-24", true),
	newNodeRelease("21.7.3", "2024-04-11", true),
	newNodeRelease("21.7.2", "2024-04-03", true),
	newNodeRelease("21.7.1", "2024-03-08", true),
	newNodeRelease("21.7.0", "2024-03-06", true),
	newNodeRelease("21.6.2", "2024-02-14", true),
	newNodeRelease("21.6.1", "2024-01-29", true),
	newNodeRelease("21.6.0", "2024-01-15", true),
	newNodeRelease("21.5.0", "2023-12-19", true),
	newNodeRelease("21.4.0", "2023-12-05", true),
	newNodeRelease("21.3.0", "2023-11-30", true),
	newNodeRelease("21.2.0", "2023-11-15", true),
	newNodeRelease("21.1.0", "2023-10-24", true),
	newNodeRelease("21.0.0", "2023-10-17", true),
	newNodeRelease("20.18.2", "2025-01-22", true),
	newNodeRelease("20.18.1", "2024-11-20", true),
	newNodeRelease("20.18.0", "2024-10-03", true),
	newNodeRelease("20.17.0", "2024-08-21", true),
	newNodeRelease("20.16.0", "2024-07-24", true),
	newNodeRelease("20.15.1", "2024-07-08", true),
	newNodeRelease("20.15.0", "2024-06-24", true),
	newNodeRelease("20.14.0", "2024-05-28", true),
	newNodeRelease("20.13.1", "2024-05-09", true),
	newNodeRelease("20.13.0", "2024-05-07", true),
	newNodeRelease("20.12.2", "2024-04-10", true),
	newNodeRelease("20.12.1", "2024-04-03", true),
	newNodeRelease("20.12.0", "2024-03-26", true),
	newNodeRelease("20.11.1", "2024-02-15", true),
	newNodeRelease("20.11.0", "2024-01-10", true),
	newNodeRelease("20.10.0", "2023-11-22", true),
	newNodeRelease("20.9.0", "2023-10-24", true),
	newNodeRelease("20.8.1", "2023-10-13", true),
	newNodeRelease("20.8.0", "2023-09-29", true),
	newNodeRelease("20.7.0", "2023-09-18", true),
	newNodeRelease("20.6.1", "2023-09-08", true),
	newNodeRelease("20.6.0", "2023-09-04", true),
	newNodeRelease("20.5.1", "2023-08-09", true),
	newNodeRelease("20.5.0", "2023-07-21", true),
	newNodeRelease("20.4.0", "2023-07-07", true),
	newNodeRelease("20.3.1", "2023-06-20", true),
	newNodeRelease("20.3.0", "2023-06-09", true),
	newNodeRelease("20.2.0", "2023-05-16", true),
	newNodeRelease("20.1.0", "2023-05-04", true),
	newNodeRelease("20.0.0", "2023-04-19", true),
	newNodeRelease("19.9.0", "2023-04-11", true),
	newNodeRelease("19.8.1", "2023-03-15", true),
	newNodeRelease("19.7.0", "2023-02-21", true),
	newNodeRelease("19.6.1", "2023-02-17", true),
	newNodeRelease("19.6.0", "2023-02-02", true),
	newNodeRelease("19.5.0", "2023-01-24", true),
	newNodeRelease("19.4.0", "2023-01-06", true),
	newNodeRelease("19.3.0", "2022-12-14", true),
	newNodeRelease("19.2.0", "2022-11-29", true),
	newNodeRelease("19.1.0", "2022-11-14", true),
	newNodeRelease("19.0.1", "2022-11-04", true),
	newNodeRelease("19.0.0", "2022-10-18", true),
	newNodeRelease("18.20.6", "2025-01-22", true),
	newNodeRelease("18.20.5", "2024-11-15", true),
	newNodeRelease("18.20.4", "2024-07-09", true),
	newNodeRelease("18.20.3", "2024-05-21", true),
	newNodeRelease("18.20.2", "2024-04-10", true),
	newNodeRelease("18.20.1", "2024-04-03", true),
	newNodeRelease("18.20.0", "2024-03-26", true),
	newNodeRelease("18.19.1", "2024-02-15", true),
	newNodeRelease("18.19.0", "2023-12-01", true),
	newNodeRelease("18.18.2", "2023-10-14", true),
	newNodeRelease("18.18.1", "2023-10-11", true),
	newNodeRelease("18.18.0", "2023-09-19", true),
	newNodeRelease("18.17.1", "2023-08-10", true),
	newNodeRelease("18.17.0", "2023-07-18", true),
	newNodeRelease("18.16.1", "2023-06-21", true),
	newNodeRelease("18.16.0", "2023-04-13", true),
	newNodeRelease("18.15.0", "2023-03-07", true),
	newNodeRelease("18.14.2", "2023-02-21", true),
	newNodeRelease("18.14.1", "2023-02-19", true),
	newNodeRelease("18.14.0", "2023-02-02", true),
	newNodeRelease("18.13.0", "2023-01-06", true),
	newNodeRelease("18.12.1", "2022-11-05", true),
	newNodeRelease("18.12.0", "2022-10-25", true),
	newNodeRelease("18.11.0", "2022-10-13", true),
	newNodeRelease("18.10.0", "2022-09-28", true),
	newNodeRelease("18.9.1", "2022-09-23", true),
	newNodeRelease("18.9.0", "2022-09-08", true),
	newNodeRelease("18.8.0", "2022-08-24", true),
	newNodeRelease("18.7.0", "2022-07-26", true),
	newNodeRelease("18.6.0", "2022-07-13", true),
	newNodeRelease("18.5.0", "2022-07-07", true),
	newNodeRelease("18.4.0", "2022-06-16", true),
	newNodeRelease("18.3.0", "2022-06-02", true),
	newNodeRelease("18.2.0", "2022-05-17", true),
	newNodeRelease("18.1.0", "2022-05-03", true),
	newNodeRelease("18.0.0", "2022-04-19", true),
	newNodeRelease("17.9.1", "2022-06-03", true),
	newNodeRelease("17.9.0", "2022-04-12", true),
	newNodeRelease("17.8.0", "2022-03-22", true),
	newNodeRelease("17.7.2", "2022-03-18", true),
	newNodeRelease("17.7.1", "2022-03-10", true),
	newNodeRelease("17.7.0", "2022-03-09", true),
	newNodeRelease("17.6.0", "2022-02-23", true),
	newNodeRelease("17.5.0", "2022-02-10", true),
	newNodeRelease("17.4.0", "2022-01-18", true),
	newNodeRelease("17.3.1", "2022-01-11", true),
	newNodeRelease("17.3.0", "2021-12-17", true),
	newNodeRelease("17.2.0", "2021-11-30", true),
	newNodeRelease("17.1.0", "2021-11-09", true),
	newNodeRelease("17.0.1", "2021-10-20", true),
	newNodeRelease("17.0.0", "2021-10-19", true),
	newNodeRelease("16.20.2", "2023-08-09", true),
	newNodeRelease("16.20.1", "2023-06-21", true),
	newNodeRelease("16.20.0", "2023-03-29", true),
	newNodeRelease("16.19.1", "2023-02-19", true),
	newNodeRelease("16.19.0", "2022-12-13", true),
	newNodeRelease("16.18.1", "2022-11-04", true),
	newNodeRelease("16.18.0", "2022-10-12", true),
	newNodeRelease("16.17.1", "2022-09-23", true),
	newNodeRelease("16.17.0", "2022-08-16", true),
	newNodeRelease("16.16.0", "2022-07-07", true),
	newNodeRelease("16.15.1", "2022-06-01", true),
	newNodeRelease("16.15.0", "2022-04-27", true),
	newNodeRelease("16.14.2", "2022-03-18", true),
	newNodeRelease("16.14.1", "2022-03-17", true),
	newNodeRelease("16.14.0", "2022-02-08", true),
	newNodeRelease("16.13.2", "2022-01-11", true),
	newNodeRelease("16.13.1", "2021-12-01", true),
	newNodeRelease("16.13.0", "2021-10-26", true),
	newNodeRelease("16.12.0", "2021-10-20", true),
	newNodeRelease("16.11.1", "2021-10-12", true),
	newNodeRelease("16.11.0", "2021-10-11", true),
	newNodeRelease("16.10.0", "2021-09-22", true),
	newNodeRelease("16.9.1", "2021-09-10", true),
	newNodeRelease("16.9.0", "2021-09-07", true),
	newNodeRelease("16.8.0", "2021-08-25", true),
	newNodeRelease("16.7.0", "2021-08-18", true),
	newNodeRelease("16.6.2", "2021-08-11", true),
	newNodeRelease("16.6.1", "2021-08-03", true),
	newNodeRelease("16.6.0", "2021-07-29", true),
	newNodeRelease("16.5.0", "2021-07-14", true),
	newNodeRelease("16.4.2", "2021-07-06", true),
	newNodeRelease("16.4.1", "2021-07-05", true),
	newNodeRelease("16.4.0", "2021-06-24", true),
	newNodeRelease("16.3.0", "2021-06-03", true),
	newNodeRelease("16.2.0", "2021-05-19", true),
	newNodeRelease("16.1.0", "2021-05-05", true),
	newNodeRelease("16.0.0", "2021-04-22", true),
	newNodeRelease("15.14.0", "2021-04-07", true),
	newNodeRelease("15.13.0", "2021-03-31", true),
	newNodeRelease("15.12.0", "2021-03-17", true),
	newNodeRelease("15.11.0", "2021-03-03", true),
	newNodeRelease("15.10.0", "2021-02-23", true),
	newNodeRelease("15.9.0", "2021-02-18", true),
	newNodeRelease("15.8.0", "2021-02-02", true),
	newNodeRelease("15.7.0", "2021-01-26", true),
	newNodeRelease("15.6.0", "2021-01-15", true),
	newNodeRelease("15.5.1", "2021-01-04", true),
	newNodeRelease("15.5.0", "2020-12-22", true),
	newNodeRelease("15.4.0", "2020-12-09", true),
	newNodeRelease("15.3.0", "2020-11-24", true),
	newNodeRelease("15.2.1", "2020-11-16", true),
	newNodeRelease("15.2.0", "2020-11-10", true),
	newNodeRelease("15.1.0", "2020-11-04", true),
	newNodeRelease("15.0.1", "2020-10-21", true),
	newNodeRelease("15.0.0", "2020-10-20", true),
	newNodeRelease("14.21.3", "2023-02-17", true),
	newNodeRelease("14.21.2", "2022-12-13", true),
	newNodeRelease("14.21.1", "2022-11-08", true),
	newNodeRelease("14.21.0", "2022-11-01", true),
	newNodeRelease("14.20.1", "2022-09-23", true),
	newNodeRelease("14.20.0", "2022-07-07", true),
	newNodeRelease("14.19.3", "2022-05-17", true),
	newNodeRelease("14.19.2", "2022-05-09", true),
	newNodeRelease("14.19.1", "2022-03-18", true),
	newNodeRelease("14.19.0", "2022-02-01", true),
	newNodeRelease("14.18.3", "2022-01-10", true),
	newNodeRelease("14.18.2", "2021-11-30", true),
	newNodeRelease("14.18.1", "2021-10-12", true),
	newNodeRelease("14.18.0", "2021-09-28", true),
	newNodeRelease("14.17.6", "2021-08-31", true),
	newNodeRelease("14.17.5", "2021-08-11", true),
	newNodeRelease("14.17.4", "2021-07-29", true),
	newNodeRelease("14.17.3", "2021-07-05", true),
	newNodeRelease("14.17.2", "2021-07-05", true),
	newNodeRelease("14.17.1", "2021-06-15", true),
	newNodeRelease("14.17.0", "2021-05-11", true),
	newNodeRelease("14.16.1", "2021-04-06", true),
	newNodeRelease("14.16.0", "2021-02-23", true),
	newNodeRelease("14.15.5", "2021-02-09", true),
	newNodeRelease("14.15.4", "2021-01-04", true),
	newNodeRelease("14.15.3", "2020-12-17", true),
	newNodeRelease("14.15.2", "2020-12-16", true),
	newNodeRelease("14.15.1", "2020-11-16", true),
	newNodeRelease("14.15.0", "2020-10-27", true),
	newNodeRelease("14.14.0", "2020-10-16", true),
	newNodeRelease("14.13.1", "2020-10-07", true),
	newNodeRelease("14.13.0", "2020-09-29", true),
	newNodeRelease("14.12.0", "2020-09-22", true),
	newNodeRelease("14.11.0", "2020-09-15", true),
	newNodeRelease("14.10.1", "2020-09-10", true),
	newNodeRelease("14.10.0", "2020-09-08", true),
	newNodeRelease("14.9.0", "2020-08-27", true),
	newNodeRelease("14.8.0", "2020-08-11", true),
	newNodeRelease("14.7.0", "2020-08-10", true),
	newNodeRelease("14.6.0", "2020-07-21", true),
	newNodeRelease("14.5.0", "2020-06-30", true),
	newNodeRelease("14.4.0", "2020-06-02", true),
	newNodeRelease("14.3.0", "2020-05-19", true),
	newNodeRelease("14.2.0", "2020-05-05", true),
	newNodeRelease("14.1.0", "2020-04-29", true),
	newNodeRelease("14.0.0", "2020-04-23", true),
	newNodeRelease("13.14.0", "2020-04-29", true),
	newNodeRelease("13.13.0", "2020-04-14", true),
	newNodeRelease("13.12.0", "2020-03-26", true),
	newNodeRelease("13.11.0", "2020-03-12", true),
	newNodeRelease("13.10.1", "2020-03-05", true),
	newNodeRelease("13.10.0", "2020-03-04", false),
	newNodeRelease("13.9.0", "2020-02-18", false),
	newNodeRelease("13.8.0", "2020-02-07", true),
	newNodeRelease("13.7.0", "2020-01-21", true),
	newNodeRelease("13.6.0", "2020-01-07", true),
	newNodeRelease("13.5.0", "2019-12-18", true),
	newNodeRelease("13.4.0", "2019-12-18", true),
	newNodeRelease("13.3.0", "2019-12-03", true),
	newNodeRelease("13.2.0", "2019-11-22", true),
	newNodeRelease("13.1.0", "2019-11-06", true),
	newNodeRelease("13.0.1", "2019-10-23", true),
	newNodeRelease("13.0.0", "2019-10-22", true),
	newNodeRelease("12.22.12", "2022-04-05", true),
	newNodeRelease("12.22.11", "2022-03-17", true),
	newNodeRelease("12.22.10", "2022-02-01", true),
	newNodeRelease("12.22.9", "2022-01-11", true),
	newNodeRelease("12.22.8", "2021-12-16", true),
	newNodeRelease("12.22.7", "2021-10-12", true),
	newNodeRelease("12.22.6", "2021-08-31", true),
	newNodeRelease("12.22.5", "2021-08-11", true),
	newNodeRelease("12.22.4", "2021-07-29", true),
	newNodeRelease("12.22.3", "2021-07-05", true),
	newNodeRelease("12.22.2", "2021-07-05", true),
	newNodeRelease("12.22.1", "2021-04-06", true),
	newNodeRelease("12.22.0", "2021-03-30", true),
	newNodeRelease("12.21.0", "2021-02-23", true),
	newNodeRelease("12.20.2", "2021-02-10", true),
	newNodeRelease("12.20.1", "2021-01-04", true),
	newNodeRelease("12.20.0", "2020-11-24", true),
	newNodeRelease("12.19.1", "2020-11-16", true),
	newNodeRelease("12.19.0", "2020-10-06", true),
	newNodeRelease("12.18.4", "2020-09-15", true),
	newNodeRelease("12.18.3", "2020-07-22", true),
	newNodeRelease("12.18.2", "2020-06-30", true),
	newNodeRelease("12.18.1", "2020-06-17", true),
	newNodeRelease("12.18.0", "2020-06-02", true),
	newNodeRelease("12.17.0", "2020-05-26", true),
	newNodeRelease("12.16.3", "2020-04-28", true),
	newNodeRelease("12.16.2", "2020-04-08", true),
	newNodeRelease("12.16.1", "2020-02-18", true),
	newNodeRelease("12.16.0", "2020-02-11", true),
	newNodeRelease("12.15.0", "2020-02-06", true),
	newNodeRelease("12.14.1", "2020-01-07", true),
	newNodeRelease("12.14.0", "2019-12-17", true),
	newNodeRelease("12.13.1", "2019-11-19", true),
	newNodeRelease("12.13.0", "2019-10-21", true),
	newNodeRelease("12.12.0", "2019-10-11", true),
	newNodeRelease("12.11.1", "2019-10-01", true),
	newNodeRelease("12.11.0", "2019-09-30", false),
	newNodeRelease("12.10.0", "2019-09-25", true),
	newNodeRelease("12.9.1", "2019-10-01", true),
	newNodeRelease("12.9.0", "2019-08-20", true),
	newNodeRelease("12.8.1", "2019-08-15", true),
	newNodeRelease("12.8.0", "2019-09-16", true),
	newNodeRelease("12.7.0", "2019-07-23", true),
	newNodeRelease("12.6.0", "2019-07-03", true),
	newNodeRelease("12.5.0", "2019-06-27", true),
	newNodeRelease("12.4.0", "2019-06-04", true),
	newNodeRelease("12.3.1", "2019-05-22", true),
	newNodeRelease("12.3.0", "2019-05-21", true),
	newNodeRelease("12.2.0", "2019-05-07", true),
	newNodeRelease("12.1.0", "2019-04-29", true),
	newNodeRelease("12.0.0", "2019-04-23", true),
	newNodeRelease("11.15.0", "2019-04-30", true),
	newNodeRelease("11.14.0", "2019-04-23", true),
	newNodeRelease("10.24.1", "2021-04-06", true),
	newNodeRelease("10.24.0", "2021-02-23", true),
	newNodeRelease("10.23.3", "2021-02-09", true),
	newNodeRelease("10.23.2", "2021-01-26", true),
	newNodeRelease("10.23.1", "2021-01-04", true),
	newNodeRelease("10.23.0", "2020-10-27", true),
	newNodeRelease("10.22.1", "2020-09-15", true),
	newNodeRelease("10.22.0", "2020-07-21", true),
	newNodeRelease("10.21.0", "2020-06-02", true),
	newNodeRelease("10.20.1", "2020-04-12", true),
	newNodeRelease("10.20.0", "2020-04-08", true),
	newNodeRelease("10.19.0", "2020-02-06", true),
	newNodeRelease("10.18.1", "2020-01-09", true),
	newNodeRelease("10.18.0", "2019-12-17", true),
	newNodeRelease("10.17.0", "2019-10-22", true),
	newNodeRelease("10.16.3", "2019-08-15", true),
	newNodeRelease("10.16.2", "2019-08-06", true),
	newNodeRelease("10.16.1", "2019-07-31", true),
	newNodeRelease("10.16.0", "2019-05-28", true),
	newNodeRelease("8.17.0", "2019-12-18", true),
	newNodeRelease("8.16.2", "2019-10-09", true),
	newNodeRelease("8.16.1", "2019-08-15", true),
	newNodeRelease("8.16.0", "2019-07-25", true),
}
