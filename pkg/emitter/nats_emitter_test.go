//
// Copyright 2022 The GUAC Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package emitter

import (
	"context"
	"errors"
	"fmt"
	"testing"

	jsoniter "github.com/json-iterator/go"

	uuid "github.com/gofrs/uuid"
	nats_test "github.com/guacsec/guac/internal/testing/nats"
	"github.com/guacsec/guac/pkg/handler/processor"
	"github.com/guacsec/guac/pkg/logging"
	"github.com/nats-io/nats.go"
)

var (
	json = jsoniter.ConfigCompatibleWithStandardLibrary
	// Taken from: https://slsa.dev/provenance/v0.1#example
	ite6SLSA = `
	{
		"_type": "https://in-toto.io/Statement/v0.1",
		"subject": [{"name": "helloworld", "digest": {"sha256": "5678..."}}],
		"predicateType": "https://slsa.dev/provenance/v0.2",
		"predicate": {
			"builder": { "id": "https://github.com/Attestations/GitHubHostedActions@v1" },
			"buildType": "https://github.com/Attestations/GitHubActionsWorkflow@v1",
			"invocation": {
			  "configSource": {
				"uri": "git+https://github.com/curl/curl-docker@master",
				"digest": { "sha1": "d6525c840a62b398424a78d792f457477135d0cf" },
				"entryPoint": "build.yaml:maketgz"
			  }
			},
			"metadata": {
			  "buildStartedOn": "2020-08-19T08:38:00Z",
			  "completeness": {
				  "environment": true
			  }
			},
			"materials": [
			  {
				"uri": "git+https://github.com/curl/curl-docker@master",
				"digest": { "sha1": "d6525c840a62b398424a78d792f457477135d0cf" }
			  }, {
				"uri": "github_hosted_vm:ubuntu-18.04:20210123.1",
				"digest": { "sha1": "d6525c840a62b398424a78d792f457477135d0cf" }
			  }
			]
		}
	}`

	ite6SLSADoc = processor.Document{
		Blob:   []byte(ite6SLSA),
		Type:   processor.DocumentITE6SLSA,
		Format: processor.FormatJSON,
		SourceInformation: processor.SourceInformation{
			Collector: "TestCollector",
			Source:    "TestSource",
		},
	}
)

func TestNatsEmitter_RecreateStream(t *testing.T) {
	natsTest := nats_test.NewNatsTestServer()
	url, err := natsTest.EnableJetStreamForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer natsTest.Shutdown()

	ctx := context.Background()
	jetStream := NewJetStream(url, "", "")
	if err := jetStream.JetStreamInit(ctx); err != nil {
		t.Fatalf("unexpected error initializing jetstream: %v", err)
	}
	defer jetStream.Close()
	tests := []struct {
		name           string
		deleteStream   bool
		wantErrMessage error
	}{{
		name:           "no new stream",
		deleteStream:   false,
		wantErrMessage: nats.ErrStreamNotFound,
	}, {
		name:           "delete stream and recreate",
		deleteStream:   true,
		wantErrMessage: nats.ErrStreamNotFound,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.deleteStream {
				err := jetStream.js.DeleteStream(streamName)
				if err != nil {
					t.Errorf("failed to delete stream: %v", err)
				}
				_, err = jetStream.js.StreamInfo(streamName)
				if err == nil || (err != nil) && !errors.Is(err, tt.wantErrMessage) {
					t.Errorf("RecreateStream() error = %v, wantErr %v", err, tt.wantErrMessage)
					return
				}
			}
			err = jetStream.RecreateStream(ctx)
			if err != nil {
				t.Fatalf("unexpected error recreating jetstream: %v", err)
			}
			_, err = jetStream.js.StreamInfo(streamName)
			if err != nil {
				t.Errorf("RecreateStream() failed to create stream with error = %v", err)
				return
			}
		})
	}
}

func testPublish(ctx context.Context, d *processor.Document, pubsub *EmitterPubSub) error {
	logger := logging.FromContext(ctx)

	docByte, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("failed marshal of document: %w", err)
	}
	err = pubsub.Publish(ctx, docByte)
	if err != nil {
		return fmt.Errorf("failed to publish document on stream: %w", err)
	}
	logger.Infof("doc published: %+v", d)
	return nil
}

func testSubscribe(ctx context.Context, transportFunc func(processor.DocumentTree) error, pubsub *EmitterPubSub) error {
	logger := logging.FromContext(ctx)

	uuid, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("failed to get uuid with the following error: %w", err)
	}
	uuidString := uuid.String()
	sub, err := pubsub.Subscribe(ctx, uuidString)
	if err != nil {
		return err
	}
	processFunc := func(d []byte) error {
		doc := processor.Document{}
		err := json.Unmarshal(d, &doc)
		if err != nil {
			fmtErrString := fmt.Sprintf("[processor: %s] failed unmarshal the document bytes", uuidString)
			logger.Errorf(fmtErrString+": %v", err)
			return fmt.Errorf(fmtErrString+": %w", err)
		}

		docNode := &processor.DocumentNode{
			Document: &doc,
			Children: nil,
		}

		docTree := processor.DocumentTree(docNode)
		err = transportFunc(docTree)
		if err != nil {
			fmtErrString := fmt.Sprintf("[processor: %s] failed transportFunc", uuidString)
			logger.Errorf(fmtErrString+": %v", err)
			return fmt.Errorf(fmtErrString+": %w", err)
		}
		logger.Infof("[processor: %s] docTree Processed: %+v", uuidString, docTree.Document.SourceInformation)
		return nil
	}

	if err := sub.GetDataFromSubscriber(ctx, processFunc); err != nil {
		return fmt.Errorf("failed to get data from subscriber with error: %w", err)
	}
	if err := sub.CloseSubscriber(ctx); err != nil {
		return fmt.Errorf("failed to close subscriber with error: %w", err)
	}
	return nil
}
