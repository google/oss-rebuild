// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log"
	"os"
	"path"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/pkg/errors"
)

func isGCSPath(pth string) bool {
	return strings.HasPrefix(pth, "gs://")
}

func gcsParts(pth string) (bucket, object string) {
	pth = strings.TrimPrefix(pth, "gs://")
	pth = strings.TrimLeft(pth, "/")
	delim := strings.IndexRune(pth, '/')
	if delim == -1 {
		return pth, ""
	}
	return pth[:delim], pth[delim+1:]
}

func copyFile(ctx context.Context, c *storage.Client, src, dest string) error {
	var err error
	var srcR io.ReadCloser
	if isGCSPath(src) {
		b, o := gcsParts(src)
		s, err := c.Bucket(b).IAM().TestPermissions(ctx, []string{"storage.objects.get"})
		if err != nil {
			return err
		}
		if len(s) != 1 {
			return errors.Errorf("insufficient GCS read permissions [bucket=%s]", b)
		}
		srcR, err = c.Bucket(b).Object(o).NewReader(ctx)
		if err != nil {
			return err
		}
	} else {
		srcR, err = os.Open(src)
		if err != nil {
			return err
		}
	}
	defer srcR.Close()
	var destW io.WriteCloser
	if isGCSPath(dest) {
		b, o := gcsParts(dest)
		if strings.HasSuffix(dest, "/") {
			o = path.Join(o, path.Base(src))
		}
		destW = c.Bucket(b).Object(o).NewWriter(ctx)
		s, err := c.Bucket(b).IAM().TestPermissions(ctx, []string{"storage.objects.create"})
		if err != nil {
			return err
		}
		if len(s) != 1 {
			return errors.Errorf("insufficient GCS write permissions [bucket=%s]", b)
		}
	} else {
		pth := dest
		if strings.HasSuffix(dest, "/") {
			pth = path.Join(pth, path.Base(src))
		}
		destW, err = os.Create(pth)
		if err != nil {
			return err
		}
	}
	defer destW.Close()
	if _, err := io.Copy(destW, srcR); err != nil {
		return err
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("USAGE: gsutil [command] [args...]")
	}
	if os.Args[1][0] == '-' {
		log.Fatal("Global flags not implemented")
	}
	if os.Args[1] != "cp" {
		log.Fatal("Command not implemented")
	}
	if len(os.Args) < 4 {
		log.Fatal("USAGE: gsutil cp [srcs...] dest")
	}
	args := os.Args[2:]
	srcs, dest := args[:len(args)-1], args[len(args)-1]
	ctx := context.Background()
	if !strings.HasSuffix(dest, "/") {
		if len(srcs) == 1 {
			log.Println("NOTICE: Destination assumed to be a file")
		} else {
			log.Fatal("Destination must have trailing slash to be used as a directory")
		}
	}
	c, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	for _, src := range srcs {
		if strings.HasPrefix(src, "-") {
			log.Fatalf("Flag not implemented: %s", src)
		}
		if err := copyFile(ctx, c, src, dest); err != nil {
			log.Fatal(errors.Wrapf(err, "failed to copy [src=%s,dest=%s]", src, dest))
		}
	}
}
