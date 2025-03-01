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

	"entgo.io/ent/dialect/sql"
	"github.com/guacsec/guac/pkg/assembler/backends/ent"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/dependency"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/predicate"
	"github.com/guacsec/guac/pkg/assembler/graphql/model"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func (b *EntBackend) IsDependency(ctx context.Context, spec *model.IsDependencySpec) ([]*model.IsDependency, error) {
	funcName := "IsDependency"
	if spec == nil {
		return nil, nil
	}

	deps, err := b.client.Dependency.Query().
		Where(isDependencyQuery(spec)).
		WithPackage(withPackageVersionTree()).
		WithDependentPackageName(withPackageNameTree()).
		WithDependentPackageVersion(withPackageVersionTree()).
		Order(ent.Asc(dependency.FieldID)).
		Limit(MaxPageSize).
		All(ctx)
	if err != nil {
		return nil, errors.Wrap(err, funcName)
	}

	return collect(deps, toModelIsDependencyWithBackrefs), nil
}

func (b *EntBackend) IngestDependencies(ctx context.Context, pkgs []*model.IDorPkgInput, depPkgs []*model.IDorPkgInput, depPkgMatchType model.MatchFlags, dependencies []*model.IsDependencyInputSpec) ([]string, error) {
	// TODO: This looks like a good candidate for using BulkCreate()

	var modelIsDependencies = make([]string, len(dependencies))
	eg, ctx := errgroup.WithContext(ctx)
	for i := range dependencies {
		index := i
		pkg := *pkgs[index]
		depPkg := *depPkgs[index]
		dpmt := depPkgMatchType
		dep := *dependencies[index]
		concurrently(eg, func() error {
			p, err := b.IngestDependency(ctx, pkg, depPkg, dpmt, dep)
			if err == nil {
				modelIsDependencies[index] = p
			}
			return err
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return modelIsDependencies, nil
}

func (b *EntBackend) IngestDependency(ctx context.Context, pkg model.IDorPkgInput, depPkg model.IDorPkgInput, depPkgMatchType model.MatchFlags, dep model.IsDependencyInputSpec) (string, error) {
	funcName := "IngestDependency"

	recordID, err := WithinTX(ctx, b.client, func(ctx context.Context) (*int, error) {
		client := ent.TxFromContext(ctx)
		p, err := getPkgVersion(ctx, client.Client(), *pkg.PackageInput)
		if err != nil {
			return nil, err
		}
		query := client.Dependency.Create().
			SetPackage(p).
			SetVersionRange(dep.VersionRange).
			SetDependencyType(dependencyTypeToEnum(dep.DependencyType)).
			SetJustification(dep.Justification).
			SetOrigin(dep.Origin).
			SetCollector(dep.Collector)

		conflictColumns := []string{
			dependency.FieldPackageID,
			dependency.FieldVersionRange,
			dependency.FieldDependencyType,
			dependency.FieldJustification,
			dependency.FieldOrigin,
			dependency.FieldCollector,
		}

		var conflictWhere *sql.Predicate

		if depPkgMatchType.Pkg == model.PkgMatchTypeAllVersions {
			dpn, err := getPkgName(ctx, client.Client(), *depPkg.PackageInput)
			if err != nil {
				return nil, err
			}
			query.SetDependentPackageName(dpn)
			conflictColumns = append(conflictColumns, dependency.FieldDependentPackageNameID)
			conflictWhere = sql.And(
				sql.NotNull(dependency.FieldDependentPackageNameID),
				sql.IsNull(dependency.FieldDependentPackageVersionID),
			)
		} else {
			dpv, err := getPkgVersion(ctx, client.Client(), *depPkg.PackageInput)
			if err != nil {
				return nil, err
			}
			query.SetDependentPackageVersion(dpv)
			conflictColumns = append(conflictColumns, dependency.FieldDependentPackageVersionID)
			conflictWhere = sql.And(
				sql.IsNull(dependency.FieldDependentPackageNameID),
				sql.NotNull(dependency.FieldDependentPackageVersionID),
			)
		}

		id, err := query.
			OnConflict(
				sql.ConflictColumns(conflictColumns...),
				sql.ConflictWhere(conflictWhere),
			).
			Ignore().
			ID(ctx)
		if err != nil {
			return nil, err
		}
		return &id, nil
	})
	if err != nil {
		return "", errors.Wrap(err, funcName)
	}

	return nodeID(*recordID), nil
}

func dependencyTypeToEnum(t model.DependencyType) dependency.DependencyType {
	switch t {
	case model.DependencyTypeDirect:
		return dependency.DependencyTypeDIRECT
	case model.DependencyTypeIndirect:
		return dependency.DependencyTypeINDIRECT
	default:
		return dependency.DependencyTypeUNKNOWN
	}
}

func isDependencyQuery(filter *model.IsDependencySpec) predicate.Dependency {
	if filter == nil {
		return NoOpSelector()
	}

	predicates := []predicate.Dependency{
		optionalPredicate(filter.ID, IDEQ),
		optionalPredicate(filter.VersionRange, dependency.VersionRange),
		optionalPredicate(filter.Justification, dependency.Justification),
		optionalPredicate(filter.Origin, dependency.Origin),
		optionalPredicate(filter.Collector, dependency.Collector),
	}
	if filter.DependencyPackage != nil {
		if filter.DependencyPackage.Version == nil {
			predicates = append(predicates,
				dependency.Or(
					dependency.HasDependentPackageNameWith(packageNameQuery(filter.DependencyPackage)),
					dependency.HasDependentPackageVersionWith(packageVersionQuery(filter.DependencyPackage)),
				),
			)
		} else {
			predicates = append(predicates, dependency.HasDependentPackageVersionWith(packageVersionQuery(filter.DependencyPackage)))
		}
	}
	if filter.Package != nil {
		predicates = append(predicates, dependency.HasPackageWith(packageVersionQuery(filter.Package)))
	}

	if filter.DependencyType != nil {
		predicates = append(predicates, dependency.DependencyTypeEQ(dependencyTypeToEnum(*filter.DependencyType)))
	}

	return dependency.And(predicates...)
}
