// Copyright 2013 Mathias Monnerville and Anthony Baillard.
// All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package cloudinary

import (
	"fmt"
	"os"
	"testing"
)

func TestDial(t *testing.T) {
	if _, err := Dial("baduri::"); err == nil {
		t.Error("should fail on bad uri")
	}

	// Not a cloudinary:// URL scheme
	if _, err := Dial("http://localhost"); err == nil {
		t.Error("should fail if URL scheme different from cloudinary://")
	}

	// Missing API secret (password)?
	if _, err := Dial("cloudinary://login@cloudname"); err == nil {
		t.Error("should fail when no API secret is provided")
	}

	k := &Service{
		cloudName: "cloudname",
		apiKey:    "login",
		apiSecret: "secret",
	}
	s, err := Dial(fmt.Sprintf("cloudinary://%s:%s@%s", k.apiKey, k.apiSecret, k.cloudName))
	if err != nil {
		t.Error("expect a working service at this stage but got an error.")
	}
	if s.cloudName != k.cloudName || s.apiKey != k.apiKey || s.apiSecret != k.apiSecret {
		t.Errorf("wrong service instance, expect %v, got %v", k, s)
	}
}

func TestUploadByFile(t *testing.T) {
	s, err := Dial(os.Getenv("CLOUDINARY"))
	if err != nil {
		t.Fatal(err)
	}

	r, err := s.UploadByFile("test_logo.png", "image")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(r)
}

func TestUploadByURL(t *testing.T) {
	s, err := Dial(os.Getenv("CLOUDINARY"))
	if err != nil {
		t.Fatal(err)
	}

	r, err := s.UploadByURL("https://res.cloudinary.com/demo/image/upload/v1584624255/sample.jpg", "image")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(r)
}

func TestUploadDestroy(t *testing.T) {
	s, err := Dial(os.Getenv("CLOUDINARY"))
	if err != nil {
		t.Fatal(err)
	}

	publicID := os.Getenv("CLOUDINARY_PUBLIC_ID")
	if publicID == "" {
		r, err := s.UploadByFile("test_logo.png", "image")
		if err != nil {
			t.Fatal(err)
		}

		publicID = r.PublicID

		t.Log(publicID)
	}

	if err := s.UploadDestroy(publicID, "image"); err != nil {
		t.Fatal(err)
	}
}
