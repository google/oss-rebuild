// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package pypi describes the PyPi registry interface.
package pypi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/semver"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var registryURL = urlx.MustParse("https://pypi.org")

// Project describes a single PyPi project with multiple releases.
type Project struct {
	Info     `json:"info"`
	Releases map[string][]Artifact `json:"releases"`
}

// Release describes a single PyPi project version with multiple artifacts.
type Release struct {
	Info      `json:"info"`
	Artifacts []Artifact `json:"urls"`
}

// Info about a project.
type Info struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Homepage    string            `json:"home_page"`
	ProjectURLs map[string]string `json:"project_urls"`
}

// An Artifact is one out of the multiple files that can be included in a release.
//
// PyPi might refer to this object as a "package" which is why it has a PackageType.
type Artifact struct {
	Digests       `json:"digests"`
	Filename      string    `json:"filename"`
	Size          int64     `json:"size"`
	PackageType   string    `json:"packagetype"`
	PythonVersion string    `json:"python_version"`
	URL           string    `json:"url"`
	UploadTime    time.Time `json:"upload_time_iso_8601"`
}

// Digests are the hashes of the artifact.
type Digests struct {
	MD5    string `json:"md5"`
	SHA256 string `json:"sha256"`
}

// Registry is an PyPI package registry.
type Registry interface {
	Project(context.Context, string) (*Project, error)
	Release(context.Context, string, string) (*Release, error)
	Artifact(context.Context, string, string, string) (io.ReadCloser, error)
}

// HTTPRegistry is a Registry implementation that uses the pypi.org HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

// Project provides all API information related to the given package.
func (r HTTPRegistry) Project(ctx context.Context, pkg string) (*Project, error) {
	pathURL, err := url.Parse(path.Join("/pypi", pkg, "json"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching project")
	}
	var p Project
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Release provides all API information related to the given version of a package.
func (r HTTPRegistry) Release(ctx context.Context, pkg, version string) (*Release, error) {
	pathURL, err := url.Parse(path.Join("/pypi", pkg, version, "json"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, registryURL.ResolveReference(pathURL).String(), nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching release")
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

// Artifact provides the artifact associated with a specific package version.
func (r HTTPRegistry) Artifact(ctx context.Context, pkg, version, filename string) (io.ReadCloser, error) {
	release, err := r.Release(ctx, pkg, version)
	if err != nil {
		return nil, err
	}
	for _, artifact := range release.Artifacts {
		if artifact.Filename == filename {
			req, _ := http.NewRequest(http.MethodGet, artifact.URL, nil)
			resp, err := r.Client.Do(req)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != 200 {
				return nil, errors.Wrap(errors.New(resp.Status), "fetching artifact")
			}
			return resp.Body, nil
		}

	}
	return nil, errors.New("not found")
}

type CPythonRelease struct {
	Version semver.Semver
	Date    time.Time
}

func newCPythonRelease(version, dateStr string) CPythonRelease {
	sv, err := semver.New(version)
	if err != nil {
		panic(errors.Wrapf(err, "parsing version '%s'", version))
	}
	date, err := time.Parse(time.DateOnly, dateStr)
	if err != nil {
		panic(errors.Wrapf(err, "parsing date '%s'", dateStr))
	}
	return CPythonRelease{Version: sv, Date: date}
}

// Generated using:
// curl -s https://www.python.org/api/v2/downloads/release/ | jq -r ' map(select(.is_published==true) | {ver: (.name | capture("(?<v>[0-9]+\\.[0-9]+\\.[0-9]+)").v), date: (.release_date | split("T")[0])}) | sort_by(.ver | split(".") | map(tonumber)) | .[] | "newCPythonRelease(\"" + .ver + "\", \"" + .date + "\"),"
var PythonReleases = []CPythonRelease{
	newCPythonRelease("2.0.1", "2001-06-22"),
	newCPythonRelease("2.1.3", "2002-04-09"),
	newCPythonRelease("2.2.0", "2001-12-21"),
	newCPythonRelease("2.2.1", "2002-04-10"),
	newCPythonRelease("2.2.2", "2002-10-14"),
	newCPythonRelease("2.2.3", "2003-05-30"),
	newCPythonRelease("2.3.0", "2003-07-29"),
	newCPythonRelease("2.3.1", "2003-09-23"),
	newCPythonRelease("2.3.2", "2003-10-03"),
	newCPythonRelease("2.3.3", "2003-12-19"),
	newCPythonRelease("2.3.4", "2004-05-27"),
	newCPythonRelease("2.3.5", "2005-02-08"),
	newCPythonRelease("2.3.6", "2006-11-01"),
	newCPythonRelease("2.3.7", "2008-03-11"),
	newCPythonRelease("2.4.0", "2004-11-30"),
	newCPythonRelease("2.4.1", "2005-03-30"),
	newCPythonRelease("2.4.2", "2005-09-27"),
	newCPythonRelease("2.4.3", "2006-04-15"),
	newCPythonRelease("2.4.4", "2006-10-18"),
	newCPythonRelease("2.4.5", "2008-03-11"),
	newCPythonRelease("2.4.6", "2008-12-19"),
	newCPythonRelease("2.5.0", "2006-09-19"),
	newCPythonRelease("2.5.1", "2007-04-19"),
	newCPythonRelease("2.5.2", "2008-02-21"),
	newCPythonRelease("2.5.3", "2008-12-19"),
	newCPythonRelease("2.5.4", "2008-12-23"),
	newCPythonRelease("2.5.5", "2010-01-31"),
	newCPythonRelease("2.5.6", "2011-05-26"),
	newCPythonRelease("2.6.0", "2008-10-02"),
	newCPythonRelease("2.6.1", "2008-12-04"),
	newCPythonRelease("2.6.2", "2009-04-14"),
	newCPythonRelease("2.6.3", "2009-10-02"),
	newCPythonRelease("2.6.4", "2009-10-26"),
	newCPythonRelease("2.6.5", "2010-03-18"),
	newCPythonRelease("2.6.6", "2010-08-24"),
	newCPythonRelease("2.6.7", "2011-06-03"),
	newCPythonRelease("2.6.8", "2012-04-10"),
	newCPythonRelease("2.6.9", "2013-10-29"),
	newCPythonRelease("2.7.0", "2010-07-03"),
	newCPythonRelease("2.7.1", "2010-11-27"),
	newCPythonRelease("2.7.2", "2011-06-11"),
	newCPythonRelease("2.7.3", "2012-04-09"),
	newCPythonRelease("2.7.4", "2013-04-06"),
	newCPythonRelease("2.7.5", "2013-05-12"),
	newCPythonRelease("2.7.6", "2013-11-10"),
	newCPythonRelease("2.7.7", "2014-06-01"),
	newCPythonRelease("2.7.7", "2014-05-17"),
	newCPythonRelease("2.7.8", "2014-07-02"),
	newCPythonRelease("2.7.9", "2014-12-10"),
	newCPythonRelease("2.7.9", "2014-11-26"),
	newCPythonRelease("2.7.10", "2015-05-23"),
	newCPythonRelease("2.7.10", "2015-05-11"),
	newCPythonRelease("2.7.11", "2015-12-05"),
	newCPythonRelease("2.7.11", "2015-11-21"),
	newCPythonRelease("2.7.12", "2016-06-25"),
	newCPythonRelease("2.7.12", "2016-06-13"),
	newCPythonRelease("2.7.13", "2016-12-17"),
	newCPythonRelease("2.7.13", "2016-12-04"),
	newCPythonRelease("2.7.14", "2017-09-16"),
	newCPythonRelease("2.7.14", "2017-08-27"),
	newCPythonRelease("2.7.15", "2018-05-01"),
	newCPythonRelease("2.7.15", "2018-04-15"),
	newCPythonRelease("2.7.16", "2019-03-04"),
	newCPythonRelease("2.7.16", "2019-02-17"),
	newCPythonRelease("2.7.17", "2019-10-19"),
	newCPythonRelease("2.7.17", "2019-10-09"),
	newCPythonRelease("2.7.18", "2020-04-20"),
	newCPythonRelease("2.7.18", "2020-04-04"),
	newCPythonRelease("3.0.0", "2008-12-03"),
	newCPythonRelease("3.0.1", "2009-02-13"),
	newCPythonRelease("3.1.0", "2009-06-26"),
	newCPythonRelease("3.1.1", "2009-08-17"),
	newCPythonRelease("3.1.2", "2010-03-20"),
	newCPythonRelease("3.1.3", "2010-11-27"),
	newCPythonRelease("3.1.4", "2011-06-11"),
	newCPythonRelease("3.1.5", "2012-04-09"),
	newCPythonRelease("3.2.0", "2011-02-20"),
	newCPythonRelease("3.2.1", "2011-07-09"),
	newCPythonRelease("3.2.2", "2011-09-03"),
	newCPythonRelease("3.2.3", "2012-04-10"),
	newCPythonRelease("3.2.4", "2013-04-06"),
	newCPythonRelease("3.2.5", "2013-05-15"),
	newCPythonRelease("3.2.6", "2014-10-12"),
	newCPythonRelease("3.2.6", "2014-10-04"),
	newCPythonRelease("3.3.0", "2012-09-29"),
	newCPythonRelease("3.3.1", "2013-04-06"),
	newCPythonRelease("3.3.2", "2013-05-15"),
	newCPythonRelease("3.3.3", "2013-11-17"),
	newCPythonRelease("3.3.4", "2014-02-09"),
	newCPythonRelease("3.3.5", "2014-03-09"),
	newCPythonRelease("3.3.5", "2014-02-23"),
	newCPythonRelease("3.3.5", "2014-02-23"),
	newCPythonRelease("3.3.5", "2014-03-02"),
	newCPythonRelease("3.3.6", "2014-10-12"),
	newCPythonRelease("3.3.6", "2014-10-04"),
	newCPythonRelease("3.3.7", "2017-09-19"),
	newCPythonRelease("3.3.7", "2017-09-06"),
	newCPythonRelease("3.4.0", "2014-03-17"),
	newCPythonRelease("3.4.0", "2014-03-10"),
	newCPythonRelease("3.4.1", "2014-05-19"),
	newCPythonRelease("3.4.1", "2014-05-05"),
	newCPythonRelease("3.4.2", "2014-10-13"),
	newCPythonRelease("3.4.2", "2014-09-22"),
	newCPythonRelease("3.4.3", "2015-02-25"),
	newCPythonRelease("3.4.3", "2015-02-08"),
	newCPythonRelease("3.4.4", "2015-12-21"),
	newCPythonRelease("3.4.4", "2015-12-07"),
	newCPythonRelease("3.4.5", "2016-06-27"),
	newCPythonRelease("3.4.5", "2016-06-13"),
	newCPythonRelease("3.4.6", "2017-01-17"),
	newCPythonRelease("3.4.6", "2017-01-03"),
	newCPythonRelease("3.4.7", "2017-08-09"),
	newCPythonRelease("3.4.7", "2017-07-25"),
	newCPythonRelease("3.4.8", "2018-02-05"),
	newCPythonRelease("3.4.8", "2018-01-23"),
	newCPythonRelease("3.4.9", "2018-08-02"),
	newCPythonRelease("3.4.9", "2018-07-20"),
	newCPythonRelease("3.4.10", "2019-03-18"),
	newCPythonRelease("3.4.10", "2019-03-04"),
	newCPythonRelease("3.5.0", "2015-09-13"),
	newCPythonRelease("3.5.0", "2015-02-08"),
	newCPythonRelease("3.5.0", "2015-03-09"),
	newCPythonRelease("3.5.0", "2015-03-30"),
	newCPythonRelease("3.5.0", "2015-04-20"),
	newCPythonRelease("3.5.0", "2015-05-24"),
	newCPythonRelease("3.5.0", "2015-06-01"),
	newCPythonRelease("3.5.0", "2015-07-05"),
	newCPythonRelease("3.5.0", "2015-07-26"),
	newCPythonRelease("3.5.0", "2015-08-11"),
	newCPythonRelease("3.5.0", "2015-08-25"),
	newCPythonRelease("3.5.0", "2015-09-08"),
	newCPythonRelease("3.5.0", "2015-09-09"),
	newCPythonRelease("3.5.1", "2015-12-07"),
	newCPythonRelease("3.5.1", "2015-11-23"),
	newCPythonRelease("3.5.2", "2016-06-27"),
	newCPythonRelease("3.5.2", "2016-06-13"),
	newCPythonRelease("3.5.3", "2017-01-17"),
	newCPythonRelease("3.5.3", "2017-01-03"),
	newCPythonRelease("3.5.4", "2017-08-08"),
	newCPythonRelease("3.5.4", "2017-07-25"),
	newCPythonRelease("3.5.5", "2018-02-05"),
	newCPythonRelease("3.5.5", "2018-01-23"),
	newCPythonRelease("3.5.6", "2018-08-02"),
	newCPythonRelease("3.5.6", "2018-07-20"),
	newCPythonRelease("3.5.7", "2019-03-18"),
	newCPythonRelease("3.5.7", "2019-03-04"),
	newCPythonRelease("3.5.8", "2019-10-29"),
	newCPythonRelease("3.5.8", "2019-09-09"),
	newCPythonRelease("3.5.8", "2019-10-12"),
	newCPythonRelease("3.5.9", "2019-11-02"),
	newCPythonRelease("3.5.10", "2020-09-05"),
	newCPythonRelease("3.5.10", "2020-08-22"),
	newCPythonRelease("3.6.0", "2016-12-23"),
	newCPythonRelease("3.6.0", "2016-05-17"),
	newCPythonRelease("3.6.0", "2016-06-13"),
	newCPythonRelease("3.6.0", "2016-07-12"),
	newCPythonRelease("3.6.0", "2016-08-15"),
	newCPythonRelease("3.6.0", "2016-09-12"),
	newCPythonRelease("3.6.0", "2016-10-10"),
	newCPythonRelease("3.6.0", "2016-10-31"),
	newCPythonRelease("3.6.0", "2016-11-21"),
	newCPythonRelease("3.6.0", "2016-12-06"),
	newCPythonRelease("3.6.0", "2016-12-16"),
	newCPythonRelease("3.6.1", "2017-03-21"),
	newCPythonRelease("3.6.1", "2017-03-05"),
	newCPythonRelease("3.6.2", "2017-07-17"),
	newCPythonRelease("3.6.2", "2017-06-17"),
	newCPythonRelease("3.6.2", "2017-07-07"),
	newCPythonRelease("3.6.3", "2017-10-03"),
	newCPythonRelease("3.6.3", "2017-09-19"),
	newCPythonRelease("3.6.4", "2017-12-19"),
	newCPythonRelease("3.6.4", "2017-12-05"),
	newCPythonRelease("3.6.5", "2018-03-28"),
	newCPythonRelease("3.6.5", "2018-03-13"),
	newCPythonRelease("3.6.6", "2018-06-27"),
	newCPythonRelease("3.6.6", "2018-06-12"),
	newCPythonRelease("3.6.7", "2018-10-20"),
	newCPythonRelease("3.6.7", "2018-09-26"),
	newCPythonRelease("3.6.7", "2018-10-13"),
	newCPythonRelease("3.6.8", "2018-12-24"),
	newCPythonRelease("3.6.8", "2018-12-11"),
	newCPythonRelease("3.6.9", "2019-07-02"),
	newCPythonRelease("3.6.9", "2019-06-18"),
	newCPythonRelease("3.6.10", "2019-12-18"),
	newCPythonRelease("3.6.10", "2019-12-11"),
	newCPythonRelease("3.6.11", "2020-06-27"),
	newCPythonRelease("3.6.11", "2020-06-17"),
	newCPythonRelease("3.6.12", "2020-08-17"),
	newCPythonRelease("3.6.13", "2021-02-15"),
	newCPythonRelease("3.6.14", "2021-06-28"),
	newCPythonRelease("3.6.15", "2021-09-04"),
	newCPythonRelease("3.7.0", "2018-06-27"),
	newCPythonRelease("3.7.0", "2017-09-19"),
	newCPythonRelease("3.7.0", "2017-10-17"),
	newCPythonRelease("3.7.0", "2017-12-05"),
	newCPythonRelease("3.7.0", "2018-01-09"),
	newCPythonRelease("3.7.0", "2018-01-31"),
	newCPythonRelease("3.7.0", "2018-02-28"),
	newCPythonRelease("3.7.0", "2018-05-30"),
	newCPythonRelease("3.7.0", "2018-06-11"),
	newCPythonRelease("3.7.1", "2018-10-20"),
	newCPythonRelease("3.7.1", "2018-09-26"),
	newCPythonRelease("3.7.1", "2018-10-13"),
	newCPythonRelease("3.7.2", "2018-12-24"),
	newCPythonRelease("3.7.2", "2018-12-11"),
	newCPythonRelease("3.7.3", "2019-03-25"),
	newCPythonRelease("3.7.3", "2019-03-12"),
	newCPythonRelease("3.7.4", "2019-07-08"),
	newCPythonRelease("3.7.4", "2019-06-18"),
	newCPythonRelease("3.7.5", "2019-10-15"),
	newCPythonRelease("3.7.5", "2019-10-02"),
	newCPythonRelease("3.7.6", "2019-12-18"),
	newCPythonRelease("3.7.6", "2019-12-11"),
	newCPythonRelease("3.7.7", "2020-03-10"),
	newCPythonRelease("3.7.7", "2020-03-04"),
	newCPythonRelease("3.7.8", "2020-06-27"),
	newCPythonRelease("3.7.8", "2020-06-17"),
	newCPythonRelease("3.7.9", "2020-08-17"),
	newCPythonRelease("3.7.10", "2021-02-15"),
	newCPythonRelease("3.7.11", "2021-06-28"),
	newCPythonRelease("3.7.12", "2021-09-04"),
	newCPythonRelease("3.7.13", "2022-03-16"),
	newCPythonRelease("3.7.14", "2022-09-06"),
	newCPythonRelease("3.7.15", "2022-10-11"),
	newCPythonRelease("3.7.16", "2022-12-06"),
	newCPythonRelease("3.7.17", "2023-06-06"),
	newCPythonRelease("3.8.0", "2019-10-14"),
	newCPythonRelease("3.8.0", "2019-02-03"),
	newCPythonRelease("3.8.0", "2019-02-25"),
	newCPythonRelease("3.8.0", "2019-03-25"),
	newCPythonRelease("3.8.0", "2019-05-06"),
	newCPythonRelease("3.8.0", "2019-06-04"),
	newCPythonRelease("3.8.0", "2019-07-04"),
	newCPythonRelease("3.8.0", "2019-07-29"),
	newCPythonRelease("3.8.0", "2019-08-29"),
	newCPythonRelease("3.8.0", "2019-10-01"),
	newCPythonRelease("3.8.1", "2019-12-18"),
	newCPythonRelease("3.8.1", "2019-12-10"),
	newCPythonRelease("3.8.2", "2020-02-24"),
	newCPythonRelease("3.8.2", "2020-02-10"),
	newCPythonRelease("3.8.2", "2020-02-17"),
	newCPythonRelease("3.8.3", "2020-05-13"),
	newCPythonRelease("3.8.3", "2020-04-29"),
	newCPythonRelease("3.8.4", "2020-07-13"),
	newCPythonRelease("3.8.4", "2020-06-30"),
	newCPythonRelease("3.8.5", "2020-07-20"),
	newCPythonRelease("3.8.6", "2020-09-24"),
	newCPythonRelease("3.8.6", "2020-09-08"),
	newCPythonRelease("3.8.7", "2020-12-21"),
	newCPythonRelease("3.8.7", "2020-12-07"),
	newCPythonRelease("3.8.8", "2021-02-19"),
	newCPythonRelease("3.8.8", "2021-02-16"),
	newCPythonRelease("3.8.9", "2021-04-02"),
	newCPythonRelease("3.8.10", "2021-05-03"),
	newCPythonRelease("3.8.11", "2021-06-28"),
	newCPythonRelease("3.8.12", "2021-08-30"),
	newCPythonRelease("3.8.13", "2022-03-16"),
	newCPythonRelease("3.8.14", "2022-09-06"),
	newCPythonRelease("3.8.15", "2022-10-11"),
	newCPythonRelease("3.8.16", "2022-12-06"),
	newCPythonRelease("3.8.17", "2023-06-06"),
	newCPythonRelease("3.8.18", "2023-08-24"),
	newCPythonRelease("3.8.19", "2024-03-19"),
	newCPythonRelease("3.8.20", "2024-09-06"),
	newCPythonRelease("3.9.0", "2020-10-05"),
	newCPythonRelease("3.9.0", "2019-11-19"),
	newCPythonRelease("3.9.0", "2019-12-18"),
	newCPythonRelease("3.9.0", "2020-01-24"),
	newCPythonRelease("3.9.0", "2020-02-26"),
	newCPythonRelease("3.9.0", "2020-03-23"),
	newCPythonRelease("3.9.0", "2020-04-28"),
	newCPythonRelease("3.9.0", "2020-05-19"),
	newCPythonRelease("3.9.0", "2020-06-09"),
	newCPythonRelease("3.9.0", "2020-06-09"),
	newCPythonRelease("3.9.0", "2020-07-03"),
	newCPythonRelease("3.9.0", "2020-07-20"),
	newCPythonRelease("3.9.0", "2020-08-11"),
	newCPythonRelease("3.9.0", "2020-09-17"),
	newCPythonRelease("3.9.1", "2020-12-07"),
	newCPythonRelease("3.9.1", "2020-11-26"),
	newCPythonRelease("3.9.2", "2021-02-19"),
	newCPythonRelease("3.9.2", "2021-02-16"),
	newCPythonRelease("3.9.3", "2021-04-02"),
	newCPythonRelease("3.9.4", "2021-04-04"),
	newCPythonRelease("3.9.5", "2021-05-03"),
	newCPythonRelease("3.9.6", "2021-06-28"),
	newCPythonRelease("3.9.7", "2021-08-30"),
	newCPythonRelease("3.9.8", "2021-11-05"),
	newCPythonRelease("3.9.9", "2021-11-15"),
	newCPythonRelease("3.9.10", "2022-01-14"),
	newCPythonRelease("3.9.11", "2022-03-16"),
	newCPythonRelease("3.9.12", "2022-03-23"),
	newCPythonRelease("3.9.13", "2022-05-17"),
	newCPythonRelease("3.9.14", "2022-09-06"),
	newCPythonRelease("3.9.15", "2022-10-11"),
	newCPythonRelease("3.9.16", "2022-12-06"),
	newCPythonRelease("3.9.17", "2023-06-06"),
	newCPythonRelease("3.9.18", "2023-08-24"),
	newCPythonRelease("3.9.19", "2024-03-19"),
	newCPythonRelease("3.9.20", "2024-09-06"),
	newCPythonRelease("3.9.21", "2024-12-03"),
	newCPythonRelease("3.9.22", "2025-04-08"),
	newCPythonRelease("3.9.23", "2025-06-03"),
	newCPythonRelease("3.10.0", "2021-10-04"),
	newCPythonRelease("3.10.0", "2020-10-05"),
	newCPythonRelease("3.10.0", "2020-11-03"),
	newCPythonRelease("3.10.0", "2020-12-07"),
	newCPythonRelease("3.10.0", "2021-01-04"),
	newCPythonRelease("3.10.0", "2021-02-02"),
	newCPythonRelease("3.10.0", "2021-03-01"),
	newCPythonRelease("3.10.0", "2021-04-05"),
	newCPythonRelease("3.10.0", "2021-05-03"),
	newCPythonRelease("3.10.0", "2021-05-31"),
	newCPythonRelease("3.10.0", "2021-06-17"),
	newCPythonRelease("3.10.0", "2021-07-10"),
	newCPythonRelease("3.10.0", "2021-08-02"),
	newCPythonRelease("3.10.0", "2021-09-07"),
	newCPythonRelease("3.10.1", "2021-12-06"),
	newCPythonRelease("3.10.2", "2022-01-14"),
	newCPythonRelease("3.10.3", "2022-03-16"),
	newCPythonRelease("3.10.4", "2022-03-24"),
	newCPythonRelease("3.10.5", "2022-06-06"),
	newCPythonRelease("3.10.6", "2022-08-02"),
	newCPythonRelease("3.10.7", "2022-09-06"),
	newCPythonRelease("3.10.8", "2022-10-11"),
	newCPythonRelease("3.10.9", "2022-12-06"),
	newCPythonRelease("3.10.10", "2023-02-08"),
	newCPythonRelease("3.10.11", "2023-04-05"),
	newCPythonRelease("3.10.12", "2023-06-06"),
	newCPythonRelease("3.10.13", "2023-08-24"),
	newCPythonRelease("3.10.14", "2024-03-19"),
	newCPythonRelease("3.10.15", "2024-09-07"),
	newCPythonRelease("3.10.16", "2024-12-03"),
	newCPythonRelease("3.10.17", "2025-04-08"),
	newCPythonRelease("3.10.18", "2025-06-03"),
	newCPythonRelease("3.11.0", "2022-10-24"),
	newCPythonRelease("3.11.0", "2021-10-05"),
	newCPythonRelease("3.11.0", "2021-11-05"),
	newCPythonRelease("3.11.0", "2021-12-08"),
	newCPythonRelease("3.11.0", "2022-01-14"),
	newCPythonRelease("3.11.0", "2022-02-03"),
	newCPythonRelease("3.11.0", "2022-03-07"),
	newCPythonRelease("3.11.0", "2022-04-05"),
	newCPythonRelease("3.11.0", "2022-05-08"),
	newCPythonRelease("3.11.0", "2022-05-31"),
	newCPythonRelease("3.11.0", "2022-06-01"),
	newCPythonRelease("3.11.0", "2022-07-11"),
	newCPythonRelease("3.11.0", "2022-07-26"),
	newCPythonRelease("3.11.0", "2022-08-08"),
	newCPythonRelease("3.11.0", "2022-09-12"),
	newCPythonRelease("3.11.1", "2022-12-06"),
	newCPythonRelease("3.11.2", "2023-02-08"),
	newCPythonRelease("3.11.3", "2023-04-05"),
	newCPythonRelease("3.11.4", "2023-06-06"),
	newCPythonRelease("3.11.5", "2023-08-24"),
	newCPythonRelease("3.11.6", "2023-10-02"),
	newCPythonRelease("3.11.7", "2023-12-04"),
	newCPythonRelease("3.11.8", "2024-02-06"),
	newCPythonRelease("3.11.9", "2024-04-02"),
	newCPythonRelease("3.11.10", "2024-09-07"),
	newCPythonRelease("3.11.11", "2024-12-03"),
	newCPythonRelease("3.11.12", "2025-04-08"),
	newCPythonRelease("3.11.13", "2025-06-03"),
	newCPythonRelease("3.12.0", "2023-10-02"),
	newCPythonRelease("3.12.0", "2022-10-25"),
	newCPythonRelease("3.12.0", "2022-11-15"),
	newCPythonRelease("3.12.0", "2022-12-06"),
	newCPythonRelease("3.12.0", "2023-01-10"),
	newCPythonRelease("3.12.0", "2023-02-07"),
	newCPythonRelease("3.12.0", "2023-03-08"),
	newCPythonRelease("3.12.0", "2023-04-04"),
	newCPythonRelease("3.12.0", "2023-05-22"),
	newCPythonRelease("3.12.0", "2023-06-06"),
	newCPythonRelease("3.12.0", "2023-06-19"),
	newCPythonRelease("3.12.0", "2023-07-11"),
	newCPythonRelease("3.12.0", "2023-08-06"),
	newCPythonRelease("3.12.0", "2023-09-06"),
	newCPythonRelease("3.12.0", "2023-09-19"),
	newCPythonRelease("3.12.1", "2023-12-08"),
	newCPythonRelease("3.12.2", "2024-02-06"),
	newCPythonRelease("3.12.3", "2024-04-09"),
	newCPythonRelease("3.12.4", "2024-06-06"),
	newCPythonRelease("3.12.5", "2024-08-06"),
	newCPythonRelease("3.12.6", "2024-09-06"),
	newCPythonRelease("3.12.7", "2024-10-01"),
	newCPythonRelease("3.12.8", "2024-12-03"),
	newCPythonRelease("3.12.9", "2025-02-04"),
	newCPythonRelease("3.12.10", "2025-04-08"),
	newCPythonRelease("3.12.11", "2025-06-03"),
	newCPythonRelease("3.13.0", "2024-10-07"),
	newCPythonRelease("3.13.0", "2023-10-13"),
	newCPythonRelease("3.13.0", "2023-11-21"),
	newCPythonRelease("3.13.0", "2024-01-17"),
	newCPythonRelease("3.13.0", "2024-02-15"),
	newCPythonRelease("3.13.0", "2024-03-12"),
	newCPythonRelease("3.13.0", "2024-04-09"),
	newCPythonRelease("3.13.0", "2024-05-08"),
	newCPythonRelease("3.13.0", "2024-06-05"),
	newCPythonRelease("3.13.0", "2024-06-27"),
	newCPythonRelease("3.13.0", "2024-07-17"),
	newCPythonRelease("3.13.0", "2024-08-01"),
	newCPythonRelease("3.13.0", "2024-09-06"),
	newCPythonRelease("3.13.0", "2024-10-01"),
	newCPythonRelease("3.13.1", "2024-12-03"),
	newCPythonRelease("3.13.2", "2025-02-04"),
	newCPythonRelease("3.13.3", "2025-04-08"),
	newCPythonRelease("3.13.4", "2025-06-03"),
	newCPythonRelease("3.13.5", "2025-06-11"),
	newCPythonRelease("3.13.6", "2025-08-06"),
	newCPythonRelease("3.13.7", "2025-08-14"),
	newCPythonRelease("3.14.0", "2024-10-15"),
	newCPythonRelease("3.14.0", "2024-11-19"),
	newCPythonRelease("3.14.0", "2024-12-17"),
	newCPythonRelease("3.14.0", "2025-01-14"),
	newCPythonRelease("3.14.0", "2025-02-11"),
	newCPythonRelease("3.14.0", "2025-03-14"),
	newCPythonRelease("3.14.0", "2025-04-08"),
	newCPythonRelease("3.14.0", "2025-05-07"),
	newCPythonRelease("3.14.0", "2025-05-26"),
	newCPythonRelease("3.14.0", "2025-06-17"),
	newCPythonRelease("3.14.0", "2025-07-08"),
	newCPythonRelease("3.14.0", "2025-07-22"),
	newCPythonRelease("3.14.0", "2025-08-14"),
}

var _ Registry = &HTTPRegistry{}
