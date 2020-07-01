// Copyright 2013 Mathias Monnerville and Anthony Baillard.
// Modified 2020 Simon Partridge & Benjamin King & Habib Rosyad
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
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL      = "https://api.cloudinary.com/v1_1"
	uploadAPIFmt = baseURL + "/%s/%s/%s" // /:cloud_name/:resource_type/:method
)

// Service is the cloudinary service
// it allows uploading of images to cloudinary
type Service struct {
	client    http.Client
	cloudName string
	apiKey    string
	apiSecret string
}

// Response from calling API.
type Response struct {
	PublicID     string `json:"public_id,omitempty"`
	SecureURL    string `json:"secure_url,omitempty"`
	Version      uint   `json:"version,omitempty"`
	Format       string `json:"format,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	Size         int    `json:"bytes,omitempty"` // In bytes
	Result       string `json:"result,omitempty"`
}

// Our request type for a request being built
type request struct {
	uri    string
	method string
	buf    *bytes.Buffer
	w      *multipart.Writer
}

// Dial will use the url to connect to the Cloudinary service.
// The uri parameter must be a valid URI with the cloudinary:// scheme,
// e.g. cloudinary://api_key:api_secret@cloud_name
func Dial(uri string) (*Service, error) {
	conn, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	if conn.Scheme != "cloudinary" {
		return nil, errors.New("missing cloudinary:// scheme in URI")
	}

	secret, exists := conn.User.Password()
	if !exists {
		return nil, errors.New("no API secret provided in URI")
	}

	s := &Service{
		client:    http.Client{},
		cloudName: conn.Host,
		apiKey:    conn.User.Username(),
		apiSecret: secret,
	}

	return s, nil
}

// UploadFile will upload a file to cloudinary
func (s *Service) UploadByFile(path, resourceType string) (*Response, error) {
	// Open file path
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return UploadByIOReader(f, resourceType)
}

// UploadImageURL will add an image to cloudinary when given a URL to the image
func (s *Service) UploadByURL(addr, resourceType string) (*Response, error) {
	// Validate url
	_, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	r, err := s.newRequest(
		fmt.Sprintf(uploadAPIFmt, s.cloudName, resourceType, "upload"),
		http.MethodPost,
		nil,
	)
	if err != nil {
		return nil, err
	}

	if err = r.addFileURL(addr); err != nil {
		return nil, err
	}

	return s.do(r)
}

// UploadByIOReader upload a file to cloudinary from a reader
func UploadByIOReader(reader io.Reader, resourceType string) (*Response, error) {
	r, err := s.newRequest(
		fmt.Sprintf(uploadAPIFmt, s.cloudName, resourceType, "upload"),
		http.MethodPost,
		nil,
	)
	if err != nil {
		return nil, err
	}

	if err = r.addFile(reader); err != nil {
		return nil, err
	}

	return s.do(r)
}

// Delete a resource in Cloudinary via public ID
func (s *Service) UploadDestroy(publicID, resourceType string) error {
	r, err := s.newRequest(
		fmt.Sprintf(uploadAPIFmt, s.cloudName, resourceType, "destroy"),
		http.MethodPost,
		map[string]string{"public_id": publicID},
	)
	if err != nil {
		return err
	}

	if err := r.w.WriteField("public_id", publicID); err != nil {
		return err
	}

	resp, err := s.do(r)
	if err != nil {
		return err
	}

	if resp != nil && resp.Result == "ok" {
		return nil
	}

	return errors.New("invalid response")
}

func (s *Service) newRequest(uri, method string, params map[string]string) (*request, error) {
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)

	// Write API key
	if err := w.WriteField("api_key", s.apiKey); err != nil {
		return nil, err
	}

	// Write timestamp
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	if err := w.WriteField("timestamp", timestamp); err != nil {
		return nil, err
	}

	// Generate signature
	// BEWARE the generation of signatures is quite particular
	// See this https://cloudinary.com/documentation/upload_images#generating_authentication_signatures
	if params == nil {
		params = map[string]string{}
	}

	params["timestamp"] = fmt.Sprintf("%s", timestamp)
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, key := range keys {
		sb.WriteString(fmt.Sprintf("%s=%s", key, params[key]))
		if i < len(keys)-1 {
			sb.WriteString("&")
		}
	}

	hash := sha1.New()
	part := fmt.Sprintf("%s%s", sb.String(), s.apiSecret)

	io.WriteString(hash, part)
	if err := w.WriteField("signature", fmt.Sprintf("%x", hash.Sum(nil))); err != nil {
		return nil, err
	}

	return &request{
		buf:    buf,
		w:      w,
		method: method,
		uri:    uri,
	}, nil
}

func (r *request) addFile(data io.Reader) error {
	f, err := r.w.CreateFormFile("file", "file")
	if err != nil {
		return err
	}

	tmp, err := ioutil.ReadAll(data)
	if err != nil {
		return err
	}
	_, err = f.Write(tmp)
	return err
}

func (r *request) addFileURL(url string) error {
	return r.w.WriteField("file", url)
}

func (r *request) build() (req *http.Request, close func() error, err error) {
	err = r.w.Close()
	if err != nil {
		return nil, nil, err
	}

	req, err = http.NewRequest(r.method, r.uri, r.buf)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", r.w.FormDataContentType())

	return req, req.Body.Close, nil
}

func (s *Service) do(r *request) (*Response, error) {
	req, close, err := r.build()
	if err != nil {
		return nil, err
	}
	defer close()

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("request error: " + resp.Status + " cld rrror: " + resp.Header.Get("X-ClD-Error"))
	}

	return decode(resp)
}

func decode(resp *http.Response) (info *Response, err error) {
	info = &Response{}
	d := json.NewDecoder(resp.Body)
	if err = d.Decode(info); err != nil {
		return
	}
	return
}
