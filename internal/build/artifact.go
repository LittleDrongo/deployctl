package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const manifestSchema = 1

type Manifest struct {
	Schema           int    `json:"schema"`
	Artifact         string `json:"artifact"`
	SHA256           string `json:"sha256"`
	Version          string `json:"version"`
	ImageVersion     string `json:"image_version"`
	Commit           string `json:"commit"`
	CommitShort      string `json:"commit_short"`
	CommitDate       string `json:"commit_date"`
	Dirty            bool   `json:"dirty"`
	Repository       string `json:"repository"`
	Platform         string `json:"platform"`
	Package          string `json:"package"`
	GoVersion        string `json:"go_version"`
	DeployctlVersion string `json:"deployctl_version"`
	BuiltAt          string `json:"built_at"`
}

func ManifestPath(artifact string) string { return artifact + ".manifest.json" }

func fileSHA256(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeManifest(filename string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(filename), ".manifest-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, filename); err != nil {
		return err
	}
	return nil
}

// LoadArtifact verifies a saved binary and its adjacent manifest before reuse.
func LoadArtifact(root, name string) (Result, Manifest, error) {
	var result Result
	var manifest Manifest
	if !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}
	artifact, err := filepath.Abs(name)
	if err != nil {
		return result, manifest, err
	}
	info, err := os.Lstat(artifact)
	if err != nil {
		return result, manifest, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return result, manifest, fmt.Errorf("artifact must be a nonempty regular file: %s", artifact)
	}
	manifestName := ManifestPath(artifact)
	manifestInfo, err := os.Lstat(manifestName)
	if err != nil {
		return result, manifest, fmt.Errorf("read artifact manifest %s: %w", manifestName, err)
	}
	if !manifestInfo.Mode().IsRegular() {
		return result, manifest, fmt.Errorf("artifact manifest must be a regular file: %s", manifestName)
	}
	file, err := os.Open(manifestName)
	if err != nil {
		return result, manifest, err
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&manifest)
	if err == nil {
		var extra any
		if extraErr := decoder.Decode(&extra); extraErr != io.EOF {
			err = fmt.Errorf("manifest must contain one JSON object")
		}
	}
	closeErr := file.Close()
	if err != nil {
		return result, manifest, fmt.Errorf("decode artifact manifest: %w", err)
	}
	if closeErr != nil {
		return result, manifest, closeErr
	}
	if manifest.Schema != manifestSchema || manifest.Artifact != filepath.Base(artifact) || manifest.SHA256 == "" || manifest.Version == "" || manifest.ImageVersion == "" || !platformPattern.MatchString(manifest.Platform) {
		return result, manifest, fmt.Errorf("artifact manifest is incomplete or unsupported")
	}
	hash, err := fileSHA256(artifact)
	if err != nil {
		return result, manifest, err
	}
	if hash != manifest.SHA256 {
		return result, manifest, fmt.Errorf("artifact SHA-256 mismatch: got %s, expected %s", hash, manifest.SHA256)
	}
	result = Result{Artifact: artifact, Manifest: manifestName, Version: manifest.Version, ImageVersion: manifest.ImageVersion}
	return result, manifest, nil
}
