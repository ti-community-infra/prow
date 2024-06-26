/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// gcsupload uploads the files and folders specified
// to GCS using the Prow-defined job configuration
package main

import (
	"context"

	"github.com/sirupsen/logrus"
	"sigs.k8s.io/prow/pkg/pod-utils/downwardapi"
	"sigs.k8s.io/prow/pkg/pod-utils/options"

	"sigs.k8s.io/prow/pkg/gcsupload"
	"sigs.k8s.io/prow/pkg/logrusutil"
	"sigs.k8s.io/prow/pkg/pod-utils/gcs"
)

func main() {
	logrusutil.ComponentInit()

	o := gcsupload.NewOptions()
	if err := options.Load(o); err != nil {
		logrus.Fatalf("Could not resolve options: %v", err)
	}

	if err := o.Validate(); err != nil {
		logrus.Fatalf("Invalid options: %v", err)
	}

	spec, err := downwardapi.ResolveSpecFromEnv()
	if err != nil {
		logrus.WithError(err).Fatal("Could not resolve job spec")
	}

	ctx := context.Background()
	if err := o.Run(ctx, spec, map[string]gcs.UploadFunc{}); err != nil {
		logrus.WithError(err).Fatal("Failed to upload to GCS")
	}
}
