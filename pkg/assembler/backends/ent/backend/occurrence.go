//
// Copyright 2023 The GUAC Authors.
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

package backend

import (
	"context"
	"strconv"

	"entgo.io/ent/dialect/sql"
	"github.com/guacsec/guac/pkg/assembler/backends/ent"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/artifact"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/occurrence"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/predicate"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/sourcename"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/sourcenamespace"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/sourcetype"
	"github.com/guacsec/guac/pkg/assembler/graphql/model"
	"github.com/pkg/errors"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"golang.org/x/sync/errgroup"
)

func (b *EntBackend) IsOccurrence(ctx context.Context, query *model.IsOccurrenceSpec) ([]*model.IsOccurrence, error) {

	records, err := b.client.Occurrence.Query().
		Where(isOccurrenceQuery(query)).
		WithArtifact().
		WithPackage(func(q *ent.PackageVersionQuery) {
			q.WithName(func(q *ent.PackageNameQuery) {
				q.WithNamespace(func(q *ent.PackageNamespaceQuery) {
					q.WithPackage()
				})
			})
		}).
		WithSource(func(q *ent.SourceNameQuery) {
			q.WithNamespace(func(q *ent.SourceNamespaceQuery) {
				q.WithSourceType()
			})
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]*model.IsOccurrence, len(records))
	for i, record := range records {
		models[i] = toModelIsOccurrenceWithSubject(record)
	}

	return models, nil
}

func (b *EntBackend) IngestOccurrences(ctx context.Context, subjects model.PackageOrSourceInputs, artifacts []*model.IDorArtifactInput, occurrences []*model.IsOccurrenceInputSpec) ([]string, error) {
	models := make([]string, len(occurrences))
	eg, ctx := errgroup.WithContext(ctx)
	for i := range occurrences {
		index := i
		var subject model.PackageOrSourceInput
		if len(subjects.Packages) > 0 {
			subject = model.PackageOrSourceInput{Package: subjects.Packages[index]}
		} else {
			subject = model.PackageOrSourceInput{Source: subjects.Sources[index]}
		}
		art := artifacts[index]
		occ := occurrences[index]
		concurrently(eg, func() error {
			modelOccurrence, err := b.IngestOccurrence(ctx, subject, *art, *occ)
			if err == nil {
				models[index] = modelOccurrence
			}
			return err
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return models, nil
}

func (b *EntBackend) IngestOccurrence(ctx context.Context,
	subject model.PackageOrSourceInput,
	art model.IDorArtifactInput,
	spec model.IsOccurrenceInputSpec,
) (string, error) {
	funcName := "IngestOccurrence"

	recordID, err := WithinTX(ctx, b.client, func(ctx context.Context) (*int, error) {
		tx := ent.TxFromContext(ctx)
		client := tx.Client()
		var err error

		artRecord, err := client.Artifact.Query().
			Order(ent.Asc(artifact.FieldID)). // is order important here?
			Where(artifactQueryInputPredicates(*art.ArtifactInput)).
			Only(ctx) // should already be ingested
		if err != nil {
			return nil, err
		}

		occurrenceCreate := client.Occurrence.Create().
			SetArtifact(artRecord).
			SetJustification(spec.Justification).
			SetOrigin(spec.Origin).
			SetCollector(spec.Collector)

		occurrenceConflictColumns := []string{
			occurrence.FieldArtifactID,
			occurrence.FieldJustification,
			occurrence.FieldOrigin,
			occurrence.FieldCollector,
		}

		var conflictWhere *sql.Predicate

		if subject.Package != nil {
			pkgVersion, err := getPkgVersion(ctx, client, *subject.Package.PackageInput)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get package version")
			}
			occurrenceCreate.SetPackage(pkgVersion)
			occurrenceConflictColumns = append(occurrenceConflictColumns, occurrence.FieldPackageID)
			conflictWhere = sql.And(
				sql.NotNull(occurrence.FieldPackageID),
				sql.IsNull(occurrence.FieldSourceID),
			)
		} else if subject.Source != nil {
			srcNameID, err := upsertSource(ctx, tx, *subject.Source.SourceInput)
			if err != nil {
				return nil, err
			}
			srcID, err := strconv.Atoi(srcNameID.SourceNameID)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get Source ID")
			}
			occurrenceCreate.SetSourceID(srcID)
			occurrenceConflictColumns = append(occurrenceConflictColumns, occurrence.FieldSourceID)
			conflictWhere = sql.And(
				sql.IsNull(occurrence.FieldPackageID),
				sql.NotNull(occurrence.FieldSourceID),
			)
		} else {
			return nil, gqlerror.Errorf("%v :: %s", funcName, "subject must be either a package or source")
		}

		id, err := occurrenceCreate.
			OnConflict(
				sql.ConflictColumns(occurrenceConflictColumns...),
				sql.ConflictWhere(conflictWhere),
			).
			UpdateNewValues().
			ID(ctx)
		if err != nil {
			return nil, err
		}

		return &id, nil
	})
	if err != nil {
		return "", gqlerror.Errorf("%v :: %s", funcName, err)
	}

	// TODO: Prepare response using a resusable resolver that accounts for preloads.

	record, err := b.client.Occurrence.Query().
		Where(occurrence.ID(*recordID)).
		WithArtifact().
		WithPackage(func(q *ent.PackageVersionQuery) {
			q.WithName(func(q *ent.PackageNameQuery) {
				q.WithNamespace(func(q *ent.PackageNamespaceQuery) {
					q.WithPackage()
				})
			})
		}).
		WithSource(func(q *ent.SourceNameQuery) {
			q.WithNamespace(func(q *ent.SourceNamespaceQuery) {
				q.WithSourceType()
			})
		}).
		Only(ctx)
	if err != nil {
		return "", gqlerror.Errorf("%v :: %s", funcName, err)
	}

	//TODO optimize for only returning ID
	return nodeID(record.ID), nil
}

func isOccurrenceQuery(filter *model.IsOccurrenceSpec) predicate.Occurrence {
	if filter == nil {
		return NoOpSelector()
	}
	predicates := []predicate.Occurrence{
		optionalPredicate(filter.ID, IDEQ),
		optionalPredicate(filter.Justification, occurrence.JustificationEQ),
		optionalPredicate(filter.Origin, occurrence.OriginEQ),
		optionalPredicate(filter.Collector, occurrence.CollectorEQ),
	}

	if filter.Artifact != nil {
		predicates = append(predicates,
			occurrence.HasArtifactWith(func(s *sql.Selector) {
				if filter.Artifact != nil {
					optionalPredicate(filter.Artifact.Digest, artifact.DigestEQ)(s)
					optionalPredicate(filter.Artifact.Algorithm, artifact.AlgorithmEQ)(s)
					optionalPredicate(filter.Artifact.ID, IDEQ)(s)
				}
			}),
		)
	}

	if filter.Subject != nil {
		if filter.Subject.Package != nil {
			predicates = append(predicates, occurrence.HasPackageWith(packageVersionQuery(filter.Subject.Package)))
		} else if filter.Subject.Source != nil {
			predicates = append(predicates,
				occurrence.HasSourceWith(
					optionalPredicate(filter.Subject.Source.ID, IDEQ),
					sourcename.HasNamespaceWith(
						optionalPredicate(filter.Subject.Source.Namespace, sourcenamespace.NamespaceEQ),
						sourcenamespace.HasSourceTypeWith(
							optionalPredicate(filter.Subject.Source.Type, sourcetype.TypeEQ),
						),
					),
					optionalPredicate(filter.Subject.Source.Name, sourcename.NameEQ),
					optionalPredicate(filter.Subject.Source.Commit, sourcename.CommitEQ),
					optionalPredicate(filter.Subject.Source.Tag, sourcename.TagEQ),
				),
			)
		}
	}
	return occurrence.And(predicates...)
}
