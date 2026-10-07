package prompttemplates

import (
	"context"
	"errors"
)

var (
	ErrNotFound = errors.New("prompt template not found")
	ErrInvalid  = errors.New("invalid prompt template")
)

type Override struct {
	Key   string
	Value string
}

type Store interface {
	List(context.Context) ([]Override, error)
	Upsert(context.Context, string, string) error
	Delete(context.Context, string) error
}

// Reader is the narrow port used by prompt-producing workflows.
type Reader interface {
	Read(context.Context, string) string
}

type Service struct {
	store Store
}

func New(store Store) *Service { return &Service{store: store} }

func (service *Service) Read(ctx context.Context, key string) string {
	definition, ok := DefinitionByKey(key)
	if !ok {
		return ""
	}
	if service == nil || service.store == nil {
		return definition.DefaultValue
	}
	overrides, err := service.store.List(ctx)
	if err != nil {
		return definition.DefaultValue
	}
	for _, override := range overrides {
		if override.Key == key {
			return override.Value
		}
	}
	return definition.DefaultValue
}

func (service *Service) List(ctx context.Context) ([]Template, error) {
	if service == nil || service.store == nil {
		return nil, errors.New("prompt template storage is unavailable")
	}
	stored, err := service.store.List(ctx)
	if err != nil {
		return nil, err
	}
	overrides := make(map[string]string, len(stored))
	for _, override := range stored {
		if _, ok := DefinitionByKey(override.Key); ok {
			overrides[override.Key] = override.Value
		}
	}
	return Build(overrides), nil
}

func (service *Service) Update(ctx context.Context, key, value string) (Template, error) {
	definition, ok := DefinitionByKey(key)
	if !ok {
		return Template{}, ErrNotFound
	}
	if service == nil || service.store == nil {
		return Template{}, errors.New("prompt template storage is unavailable")
	}
	if err := Validate(definition, value); err != nil {
		return Template{}, errors.Join(ErrInvalid, err)
	}
	if value == definition.DefaultValue {
		if err := service.store.Delete(ctx, key); err != nil {
			return Template{}, err
		}
		return templateFor(key, nil), nil
	}
	if err := service.store.Upsert(ctx, key, value); err != nil {
		return Template{}, err
	}
	return templateFor(key, map[string]string{key: value}), nil
}

func templateFor(key string, overrides map[string]string) Template {
	for _, template := range Build(overrides) {
		if template.Key == key {
			return template
		}
	}
	return Template{}
}
