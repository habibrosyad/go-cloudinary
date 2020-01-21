// Copyright 2013 Mathias Monnerville and Anthony Baillard.
// All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cloudinary provides support for managing static assets
// on the Cloudinary service.
//
// The Cloudinary service allows image and raw files management in
// the cloud.
package cloudinary

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	baseUploadURL   = "https://api.cloudinary.com/v1_1"
	baseResourceURL = "https://res.cloudinary.com"
	baseAdminURL    = "https://api.cloudinary.com/v1_1"
	imageType       = "image"
	videoType       = "video"
	pdfType         = "image"
	rawType         = "raw"
)

type ResourceType int

const (
	ImageType ResourceType = iota
	PdfType
	VideoType
	RawType
)

type Service struct {
	cloudName        string
	apiKey           string
	apiSecret        string
	uploadURI        *url.URL     // To upload resources
	adminURI         *url.URL     // To use the admin API
	uploadResType    ResourceType // Upload resource type
	basePathDir      string       // Base path directory
	prependPath      string       // Remote prepend path
	verbose          bool
	simulate         bool // Dry run (NOP)
	keepFilesPattern *regexp.Regexp
}

// Resource holds information about an image or a raw file.
type Resource struct {
	PublicID     string `json:"public_id"`
	Version      int    `json:"version"`
	ResourceType string `json:"resource_type"` // image or raw
	Size         int    `json:"bytes"`         // In bytes
	URL          string `json:"url"`           // Remote url
	SecureURL    string `json:"secure_url"`    // Over https
}

type pagination struct {
	NextCursor int64 `json: "next_cursor"`
}

type resourceList struct {
	pagination
	Resources []*Resource `json: "resources"`
}

type ResourceDetails struct {
	PublicID     string     `json:"public_id"`
	Format       string     `json:"format"`
	Version      int        `json:"version"`
	ResourceType string     `json:"resource_type"` // image or raw
	Size         int        `json:"bytes"`         // In bytes
	Width        int        `json:"width"`         // Width
	Height       int        `json:"height"`        // Height
	URL          string     `json:"url"`           // Remote url
	SecureURL    string     `json:"secure_url"`    // Over https
	Derived      []*Derived `json:"derived"`       // Derived
}

type Derived struct {
	Transformation string `json:"transformation"` // Transformation
	Size           int    `json:"bytes"`          // In bytes
	URL            string `json:"url"`            // Remote url
}

// Upload response after uploading a file.
type uploadResponse struct {
	ID           string `bson:"_id"`
	PublicID     string `json:"public_id"`
	Version      uint   `json:"version"`
	Format       string `json:"format"`
	ResourceType string `json:"resource_type"` // "image" or "raw"
	Size         int    `json:"bytes"`         // In bytes
	Checksum     string // SHA1 Checksum
}

// Dial will use the url to connect to the Cloudinary service.
// The uri parameter must be a valid URI with the cloudinary:// scheme,
// e.g.
//  cloudinary://api_key:api_secret@cloud_name
func Dial(uri string) (*Service, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "cloudinary" {
		return nil, errors.New("Missing cloudinary:// scheme in URI")
	}
	secret, exists := u.User.Password()
	if !exists {
		return nil, errors.New("no API secret provided in URI")
	}
	s := &Service{
		cloudName:     u.Host,
		apiKey:        u.User.Username(),
		apiSecret:     secret,
		uploadResType: ImageType,
		simulate:      false,
		verbose:       false,
	}
	// Default upload URI to the service. Can change at runtime in the
	// Upload() function for raw file uploading.
	up, err := url.Parse(fmt.Sprintf("%s/%s/image/upload/", baseUploadURL, s.cloudName))
	if err != nil {
		return nil, err
	}
	s.uploadURI = up

	// Admin API url
	adm, err := url.Parse(fmt.Sprintf("%s/%s", baseAdminURL, s.cloudName))
	if err != nil {
		return nil, err
	}
	adm.User = url.UserPassword(s.apiKey, s.apiSecret)
	s.adminURI = adm
	return s, nil
}

// Verbose activate/desactivate debugging information on standard output.
func (s *Service) Verbose(v bool) {
	s.verbose = v
}

// Simulate show what would occur but actualy don't do anything. This is a dry-run.
func (s *Service) Simulate(v bool) {
	s.simulate = v
}

// KeepFiles sets a regex pattern of remote public ids that won't be deleted
// by any Delete() command. This can be useful to forbid deletion of some
// remote resources. This regexp pattern applies to both image and raw data
// types.
func (s *Service) KeepFiles(pattern string) error {
	if len(strings.TrimSpace(pattern)) == 0 {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	s.keepFilesPattern = re
	return nil
}

// CloudName returns the cloud name used to access the Cloudinary service.
func (s *Service) CloudName() string {
	return s.cloudName
}

// APIKey returns the API key used to access the Cloudinary service.
func (s *Service) APIKey() string {
	return s.apiKey
}

// DefaultUploadURI returns the default URI used to upload images to the Cloudinary service.
func (s *Service) DefaultUploadURI() *url.URL {
	return s.uploadURI
}

// cleanAssetName returns an asset name from the parent dirname and
// the file name without extension.
// The combination
//   path=/tmp/css/default.css
//   basePath=/tmp/
//   prependPath=new/
// will return
//   new/css/default
func cleanAssetName(path, basePath, prependPath string) string {
	var name string
	path, basePath, prependPath = strings.TrimSpace(path), strings.TrimSpace(basePath), strings.TrimSpace(prependPath)
	basePath, err := filepath.Abs(basePath)
	if err != nil {
		basePath = ""
	}
	apath, err := filepath.Abs(path)
	if err == nil {
		path = apath
	}
	if basePath == "" {
		idx := strings.LastIndex(path, string(os.PathSeparator))
		if idx != -1 {
			idx = strings.LastIndex(path[:idx], string(os.PathSeparator))
		}
		name = path[idx+1:]
	} else {
		// Directory
		name = strings.Replace(path, basePath, "", 1)
		if name[0] == os.PathSeparator {
			name = name[1:]
		}
	}
	if prependPath != "" {
		if prependPath[0] == os.PathSeparator {
			prependPath = prependPath[1:]
		}
		prependPath = EnsureTrailingSlash(prependPath)
	}
	r := prependPath + name[:len(name)-len(filepath.Ext(name))]
	return strings.Replace(r, string(os.PathSeparator), "/", -1)
}

// EnsureTrailingSlash adds a missing trailing / at the end
// of a directory name.
func EnsureTrailingSlash(dirname string) string {
	if !strings.HasSuffix(dirname, "/") {
		dirname += "/"
	}
	return dirname
}

func (s *Service) walkIt(path string, info os.FileInfo, err error) error {
	if info.IsDir() {
		return nil
	}
	if _, err := s.uploadFile(path, nil, false); err != nil {
		return err
	}
	return nil
}

// Upload file to the service. When using a mongoDB database for storing
// file information (such as checksums), the database is updated after
// any successful upload.
func (s *Service) uploadFile(fullPath string, data io.Reader, randomPublicID bool) (string, error) {
	// Do not upload empty files
	fi, err := os.Stat(fullPath)
	if err == nil && fi.Size() == 0 {
		return fullPath, nil
		if s.verbose {
			fmt.Println("Not uploading empty file: ", fullPath)
		}
	}
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)

	// Write public ID
	var publicID string
	if !randomPublicID {
		publicID = cleanAssetName(fullPath, s.basePathDir, s.prependPath)
		pi, err := w.CreateFormField("public_id")
		if err != nil {
			return fullPath, err
		}
		pi.Write([]byte(publicID))
	}
	// Write API key
	ak, err := w.CreateFormField("api_key")
	if err != nil {
		return fullPath, err
	}
	ak.Write([]byte(s.apiKey))

	// Write timestamp
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	ts, err := w.CreateFormField("timestamp")
	if err != nil {
		return fullPath, err
	}
	ts.Write([]byte(timestamp))

	// Write signature
	hash := sha1.New()
	part := fmt.Sprintf("timestamp=%s%s", timestamp, s.apiSecret)
	if !randomPublicID {
		part = fmt.Sprintf("public_id=%s&%s", publicID, part)
	}
	io.WriteString(hash, part)
	signature := fmt.Sprintf("%x", hash.Sum(nil))

	si, err := w.CreateFormField("signature")
	if err != nil {
		return fullPath, err
	}
	si.Write([]byte(signature))

	// Write file field
	fw, err := w.CreateFormFile("file", fullPath)
	if err != nil {
		return fullPath, err
	}
	if data != nil { // file descriptor given
		tmp, err := ioutil.ReadAll(data)
		if err != nil {
			return fullPath, err
		}
		fw.Write(tmp)
	} else { // no file descriptor, try opening the file
		fd, err := os.Open(fullPath)
		if err != nil {
			return fullPath, err
		}
		defer fd.Close()

		_, err = io.Copy(fw, fd)
		if err != nil {
			return fullPath, err
		}
		log.Printf("Uploading %s\n", fullPath)
	}
	// Don't forget to close the multipart writer to get a terminating boundary
	w.Close()
	if s.simulate {
		return fullPath, nil
	}

	upURI := s.uploadURI.String()

	if s.uploadResType == PdfType {
		upURI = strings.Replace(upURI, imageType, pdfType, 1)
	} else if s.uploadResType == VideoType {
		upURI = strings.Replace(upURI, imageType, videoType, 1)
	} else if s.uploadResType == RawType {
		upURI = strings.Replace(upURI, imageType, rawType, 1)
	}
	req, err := http.NewRequest("POST", upURI, buf)
	if err != nil {
		return fullPath, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fullPath, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// TODO return nil and error
		return fullPath, errors.New("Request error: " + resp.Status)
	}

	// Body is JSON data and looks like:
	// {"public_id":"Downloads/file","version":1369431906,"format":"png","resource_type":"image"}
	dec := json.NewDecoder(resp.Body)
	upInfo := new(uploadResponse)
	if err := dec.Decode(upInfo); err != nil {
		return fullPath, err
	}
	return upInfo.PublicID, nil
}

// helpers
func (s *Service) UploadStaticRaw(path string, data io.Reader, prepend string) (string, error) {
	return s.Upload(path, data, prepend, false, RawType)
}

func (s *Service) UploadStaticImage(path string, data io.Reader, prepend string) (string, error) {
	return s.Upload(path, data, prepend, false, ImageType)
}

func (s *Service) UploadRaw(path string, data io.Reader, prepend string) (string, error) {
	return s.Upload(path, data, prepend, false, RawType)
}

func (s *Service) UploadImage(path string, data io.Reader, prepend string) (string, error) {
	return s.Upload(path, data, prepend, false, ImageType)
}

func (s *Service) UploadVideo(path string, data io.Reader, prepend string) (string, error) {
	return s.Upload(path, data, prepend, false, VideoType)
}

func (s *Service) UploadPdf(path string, data io.Reader, prepend string) (string, error) {
	return s.Upload(path, data, prepend, false, PdfType)
}

// Upload a file or a set of files to the cloud. The path parameter is
// a file location or a directory. If the source path is a directory,
// all files are recursively uploaded to Cloudinary.
//
// In order to upload content, path is always required (used to get the
// directory name or resource name if randomPublicID is false) but data
// can be nil. If data is non-nil the content of the file will be read
// from it. If data is nil, the function will try to open filename(s)
// specified by path.
//
// If ramdomPublicId is true, the service generates a unique random public
// id. Otherwise, the resource's public id is computed using the absolute
// path of the file.
//
// Set rtype to the target resource type, e.g. image or raw file.
//
// For example, a raw file /tmp/css/default.css will be stored with a public
// name of css/default.css (raw file keeps its extension), but an image file
// /tmp/images/logo.png will be stored as images/logo.
//
// The function returns the public identifier of the resource.
func (s *Service) Upload(path string, data io.Reader, prepend string, randomPublicID bool, rtype ResourceType) (string, error) {
	s.uploadResType = rtype
	s.basePathDir = ""
	s.prependPath = prepend
	if data == nil {
		info, err := os.Stat(path)
		if err != nil {
			return path, err
		}

		if info.IsDir() {
			s.basePathDir = path
			if err := filepath.Walk(path, s.walkIt); err != nil {
				return path, err
			}
		} else {
			return s.uploadFile(path, nil, randomPublicID)
		}
	} else {
		return s.uploadFile(path, data, randomPublicID)
	}
	return path, nil
}

// URL returns the complete access path in the cloud to the
// resource designed by publicID or the empty string if
// no match.
func (s *Service) URL(publicID string, rtype ResourceType) string {
	path := imageType
	if rtype == PdfType {
		path = pdfType
	} else if rtype == VideoType {
		path = videoType
	} else if rtype == RawType {
		path = rawType
	}
	return fmt.Sprintf("%s/%s/%s/upload/%s", baseResourceURL, s.cloudName, path, publicID)
}

func handleHTTPResponse(resp *http.Response) (map[string]interface{}, error) {
	if resp == nil {
		return nil, errors.New("nil http response")
	}
	dec := json.NewDecoder(resp.Body)
	var msg interface{}
	if err := dec.Decode(&msg); err != nil {
		return nil, err
	}
	m := msg.(map[string]interface{})
	if resp.StatusCode != http.StatusOK {
		// JSON error looks like {"error":{"message":"Missing required parameter - public_id"}}
		if e, ok := m["error"]; ok {
			return nil, errors.New(e.(map[string]interface{})["message"].(string))
		}
		return nil, errors.New(resp.Status)
	}
	return m, nil
}

// Delete deletes a resource uploaded to Cloudinary.
func (s *Service) Delete(publicID, prepend string, rtype ResourceType) error {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	data := url.Values{
		"api_key":   []string{s.apiKey},
		"public_id": []string{prepend + publicID},
		"timestamp": []string{timestamp},
	}
	if s.keepFilesPattern != nil {
		if s.keepFilesPattern.MatchString(prepend + publicID) {
			fmt.Println("keep")
			return nil
		}
	}
	if s.simulate {
		fmt.Println("ok")
		return nil
	}

	// Signature
	hash := sha1.New()
	part := fmt.Sprintf("public_id=%s&timestamp=%s%s", prepend+publicID, timestamp, s.apiSecret)
	io.WriteString(hash, part)
	data.Set("signature", fmt.Sprintf("%x", hash.Sum(nil)))

	rt := imageType
	if rtype == RawType {
		rt = rawType
	}
	resp, err := http.PostForm(fmt.Sprintf("%s/%s/%s/destroy/", baseUploadURL, s.cloudName, rt), data)
	if err != nil {
		return err
	}

	m, err := handleHTTPResponse(resp)
	if err != nil {
		return err
	}
	if e, ok := m["result"]; ok {
		fmt.Println(e.(string))
	}

	return nil
}

func (s *Service) Rename(publicID, toPublicID, prepend string, rtype ResourceType) error {
	publicID = strings.TrimPrefix(publicID, "/")
	toPublicID = strings.TrimPrefix(toPublicID, "/")
	timestamp := fmt.Sprintf(`%d`, time.Now().Unix())
	data := url.Values{
		"api_key":        []string{s.apiKey},
		"from_public_id": []string{prepend + publicID},
		"timestamp":      []string{timestamp},
		"to_public_id":   []string{prepend + toPublicID},
	}
	// Signature
	hash := sha1.New()
	part := fmt.Sprintf("from_public_id=%s&timestamp=%s&to_public_id=%s%s", prepend+publicID, timestamp, toPublicID, s.apiSecret)
	io.WriteString(hash, part)
	data.Set("signature", fmt.Sprintf("%x", hash.Sum(nil)))

	rt := imageType
	if rtype == RawType {
		rt = rawType
	}
	resp, err := http.PostForm(fmt.Sprintf("%s/%s/%s/rename", baseUploadURL, s.cloudName, rt), data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return errors.New(string(body))
	}
	return nil
}
