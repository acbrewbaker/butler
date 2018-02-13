//+build ignore

package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"log"
	"os"
	"sync"
	"text/template"
	"time"

	humanize "github.com/dustin/go-humanize"

	"github.com/itchio/arkive/zip"
	"github.com/itchio/butler/archive/szextractor/types"
	"github.com/itchio/wharf/eos"
)

func main() {
	version := "v1.5.0"
	osarches := []string{
		"windows-386",
		"windows-amd64",
		"linux-386",
		"linux-amd64",
		"darwin-amd64",
	}
	baseURL := "https://dl.itch.ovh/libc7zip"

	log.Printf("Generating depsMap for %s", version)
	depSpecMap := make(types.DepSpecMap)
	var mapMutex sync.Mutex

	numTasks := 0
	done := make(chan bool)

	work := func(osarch string) {
		defer func() {
			done <- true
		}()

		log.Printf("Hashing files for %s", osarch)

		ds := types.DepSpec{}

		zipURL := fmt.Sprintf("%s/%s/%s/libc7zip.zip", baseURL, osarch, version)

		f, err := eos.Open(zipURL)
		must(err)
		defer f.Close()

		stats, err := f.Stat()
		must(err)

		zr, err := zip.NewReader(f, stats.Size())
		must(err)

		log.Printf("  %d files to process", len(zr.File))

		for _, f := range zr.File {
			func() {
				log.Printf("  - %s (%s)...", f.Name, humanize.IBytes(uint64(f.UncompressedSize64)))

				de := types.DepEntry{
					Name: f.Name,
				}

				r, err := f.Open()
				must(err)
				defer r.Close()

				hashes := map[types.HashAlgo]hash.Hash{
					types.HashAlgoSHA1:   sha1.New(),
					types.HashAlgoSHA256: sha256.New(),
				}

				var writers []io.Writer
				for _, h := range hashes {
					writers = append(writers, h)
				}

				mw := io.MultiWriter(writers...)

				copiedBytes, err := io.Copy(mw, r)
				must(err)

				de.Size = copiedBytes

				for algo, h := range hashes {
					de.Hashes = append(de.Hashes, types.DepHash{
						Algo:  algo,
						Value: fmt.Sprintf("%x", h.Sum(nil)),
					})
				}

				ds.Entries = append(ds.Entries, de)
			}()
		}

		ds.Sources = append(ds.Sources, zipURL)
		func() {
			mapMutex.Lock()
			defer mapMutex.Unlock()
			depSpecMap[osarch] = ds
		}()
	}

	for _, osarch := range osarches {
		numTasks++
		go work(osarch)
	}

	for i := 0; i < numTasks; i++ {
		<-done
	}

	f, err := os.Create("formulas.go")
	must(err)
	defer f.Close()

	packageTemplate.Execute(f, struct {
		Timestamp time.Time
		Version   string
		BaseURL   string
		Map       types.DepSpecMap
	}{
		Timestamp: time.Now(),
		Version:   version,
		BaseURL:   baseURL,
		Map:       depSpecMap,
	})
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

var packageTemplate = template.Must(template.New("").Parse(`// Code generated by go generate; DO NOT EDIT.
// Generated at {{ .Timestamp }}
// For version {{ .Version }}, base URL {{ .BaseURL }}
package formulas

import "github.com/itchio/butler/archive/szextractor/types"

var ByOsArch = {{ printf "%#v" .Map }}
`))
