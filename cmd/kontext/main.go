package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/v1util"

	"github.com/mattmoor/kontext/pkg/manifest"
)

var (
	directory = flag.String("directory", "", "The directory to upload as context.")
	tag       = flag.String("tag", "", "The tag to which we upload the context.")
)

const (
	BasePath     = "/var/run/kontext"
	ManifestPath = "/var/lib/kontext/manifest.json"
)

var (
	defaultBaseImage, _ = name.ParseReference(
		"gcr.io/mattmoor-public/github.com/mattmoor/kontext/cmd/extractor:latest",
		name.WeakValidation)
)

// Access *repo and attempt to identify a base image appropriate for
// use as a base image, then:
// 1. Access the Manifest from our topmost layer, parse and return it as the starting
//   value for Manifest.
// 2. Use this as the value for baseImage.
// The default base image is the self-extracting base, with an empty manifest.
func findBaseImage(targetTag name.Tag) (v1.Image, *manifest.Manifest, error) {
	fallback := func(err error) (v1.Image, *manifest.Manifest, error) {
		log.Printf("Error accessing %v, falling back on a clean slate: %v", targetTag, err)
		// Fallback on the defaultBaseImage, with an empty manifest.
		base, err := remote.Image(defaultBaseImage, remote.WithAuthFromKeychain(authn.DefaultKeychain))
		if err != nil {
			return nil, nil, err
		}
		return base, &manifest.Manifest{}, nil
	}

	// First, try to access the target image, and see if it has what we need.
	base, err := remote.Image(targetTag, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return fallback(err)
	}

	ls, err := base.Layers()
	if err != nil {
		return fallback(err)
	}

	jsonLayer := ls[len(ls)-1]
	ucl, err := jsonLayer.Uncompressed()
	if err != nil {
		return fallback(err)
	}
	defer ucl.Close()

	tr := tar.NewReader(ucl)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fallback(err)
		}
		if header.Name != ManifestPath {
			continue
		}
		m := &manifest.Manifest{}
		if err := json.NewDecoder(tr).Decode(m); err != nil {
			return fallback(err)
		}
		return base, m, nil
	}
	return fallback(fmt.Errorf("Unable to find manifest in %v", targetTag))
}

// Recursively walk the files under *directory and synthesize a chrooted layer.
// This is similar to the kodata logic here:
//   https://github.com/google/go-containerregistry/blob/7842f2e9ee14544/pkg/ko/build/gobuild.go#L174
// The major change to this is that before files are added, we determine whether
// they already exist via the Manifest.
func writeNewFiles(m *manifest.Manifest) (*bytes.Buffer, int, error) {
	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	defer tw.Close()

	count := 0

	var allPaths []string
	err := filepath.Walk(*directory,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Chase symlinks.
			info, err = os.Stat(path)
			if err != nil {
				return err
			}

			// Get the info we need from the local file.
			i, err := manifest.Value(path, info)
			if err != nil {
				return err
			}

			// Compute the path relative to the base path
			relativePath := "."
			if path != *directory {
				relativePath = path[len(*directory)+1:]
			}

			// Regardless of whether we add it, it's part of
			// our context, so add it to the list of seen paths.
			allPaths = append(allPaths, relativePath)

			// Check for the file as-is in our base image.
			// If it exists, then skip it.
			// Otherwise adds its info to the new Manifest and include
			// it into our layer tarball.
			if m.Has(relativePath) {
				return nil
			}
			count++
			m.Add(relativePath, i)
			newPath := filepath.Join(BasePath, relativePath)

			if info.Mode().IsDir() {
				return tw.WriteHeader(&tar.Header{
					Name:     newPath,
					Typeflag: tar.TypeDir,
					Mode:     0555,
				})
			}

			// Open the file to copy it into the tarball.
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			// Copy the file into the image tarball.
			if err := tw.WriteHeader(&tar.Header{
				Name:     newPath,
				Size:     info.Size(),
				Typeflag: tar.TypeReg,
				// Use a fixed Mode, so that this isn't sensitive to the directory and umask
				// under which it was created. Additionally, windows can only set 0222,
				// 0444, or 0666, none of which are executable.
				Mode: 0555,
			}); err != nil {
				return err
			}
			_, err = io.Copy(tw, file)
			if err != nil {
				return err
			}
			return nil
		})
	if err != nil {
		return nil, -1, err
	}

	// For each file present in the manifest that no longer exists, we
	// should synthesize a whiteout file.
	for _, missing := range m.Missing(allPaths) {
		newPath := filepath.Clean(filepath.Join(BasePath, missing))
		dir, base := filepath.Dir(newPath), filepath.Base(newPath)
		base = fmt.Sprintf(".wh.%s", base)
		newPath = filepath.Join(dir, base)

		count++
		m.Remove(missing)
		if !m.Has(filepath.Dir(missing)) {
			continue
		}
		// Only write the whiteout file, if the containing directory is still present.
		if err := tw.WriteHeader(&tar.Header{
			Name:     newPath,
			Size:     0,
			Typeflag: tar.TypeReg,
		}); err != nil {
			return nil, -1, err
		}
	}

	return buf, count, nil
}

// Turn `m` into a layer with a single file.
// Note: we use a separate layer for the manifest (vs. including in #1) to
// avoid downloading more data than needed to access it for incremental
// re-uploads.
func writeManifest(m *manifest.Manifest) (*bytes.Buffer, error) {
	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	defer tw.Close()

	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	header := &tar.Header{
		Name:     ManifestPath,
		Size:     int64(len(b)),
		Typeflag: tar.TypeReg,
		// Use a fixed Mode, so that this isn't sensitive to the directory and umask
		// under which it was created. Additionally, windows can only set 0222,
		// 0444, or 0666, none of which are executable.
		Mode: 0555,
	}
	// write the header to the tarball archive
	if err := tw.WriteHeader(header); err != nil {
		return nil, err
	}
	// copy the json data to the tarball
	if _, err := tw.Write(b); err != nil {
		return nil, err
	}

	return buf, nil
}

func combineImage(base v1.Image, layer, mlayer *bytes.Buffer) (v1.Image, error) {
	dataLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return v1util.NopReadCloser(bytes.NewBuffer(layer.Bytes())), nil
	})
	if err != nil {
		return nil, err
	}

	jsonLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return v1util.NopReadCloser(bytes.NewBuffer(mlayer.Bytes())), nil
	})
	if err != nil {
		return nil, err
	}

	// Augment the base image with our application layer.
	return mutate.AppendLayers(base, dataLayer, jsonLayer)
}

func main() {
	flag.Parse()

	if *directory == "" {
		log.Fatalf("Missing required flag: --directory")
	}

	if *tag == "" {
		log.Fatalf("Missing required flag: --repo")
	}
	targetTag, err := name.NewTag(*tag, name.WeakValidation)
	if err != nil {
		log.Fatalf("Invalid repo %q: %v", *tag, err)
	}

	base, m, err := findBaseImage(targetTag)
	if err != nil {
		log.Fatalf("Error finding base image: %v", err)
	}

	// TODO(mattmoor): Consider applying heuristics around *whether* to use this base image
	// (or some subset thereof) or whether to fallback on the clean base.

	layer, delta, err := writeNewFiles(m)
	if err != nil {
		log.Fatalf("Error computing delta layer: %v", err)
	}

	// TODO(mattmoor): We should publish base to targetTag in this case to make sure targetTag
	// always has the image.  Although, perhaps we'll always create an entry for the empty
	// directory itself?  This warrants some experimentation.
	if delta == 0 {
		log.Println("No change in source context (or empty)")
		return
	}

	mlayer, err := writeManifest(m)
	if err != nil {
		log.Fatalf("Error writing manifest layer: %v", err)
	}

	// We must append the two synthesized layers to the base.
	combinedImage, err := combineImage(base, layer, mlayer)
	if err != nil {
		log.Fatalf("Error appending layers: %v", err)
	}

	// Publish the resulting image as targetTag.
	auth, err := authn.DefaultKeychain.Resolve(targetTag.Registry)
	if err != nil {
		log.Fatalf("Error getting creds for %q: %v", targetTag, err)
	}

	if err := remote.Write(targetTag, combinedImage, auth, http.DefaultTransport); err != nil {
		log.Fatalf("Error publishing image: %v", err)
	}
	log.Printf("Published %v!", targetTag)
}
