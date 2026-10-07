package prompttemplates

import (
	"context"
	"testing"
)

func TestDefaultsValidateAndRender(t *testing.T) {
	if err := ValidateDefaults(); err != nil {
		t.Fatal(err)
	}
	rendered := Render("Hello {{ issue_title }} {{issue_body}}", map[string]string{"issue_title": "Bug", "issue_body": "Details"})
	if rendered != "Hello Bug Details" {
		t.Fatalf("rendered = %q", rendered)
	}
}

func TestValidateRejectsUnsupportedPlaceholder(t *testing.T) {
	definition, ok := DefinitionByKey(IssueAgentPlanKey)
	if !ok {
		t.Fatal("missing definition")
	}
	if err := Validate(definition, "{{unknown}}"); err == nil {
		t.Fatal("expected unsupported placeholder error")
	}
}

func TestBuildUsesEmptyVariableSlices(t *testing.T) {
	for _, template := range Build(map[string]string{}) {
		if template.Variables == nil {
			t.Fatalf("template %s variables are nil", template.Key)
		}
	}
}

type templateStore struct {
	values map[string]string
}

func (store *templateStore) List(context.Context) ([]Override, error) {
	result := []Override{}
	for key, value := range store.values {
		result = append(result, Override{Key: key, Value: value})
	}
	return result, nil
}
func (store *templateStore) Upsert(_ context.Context, key, value string) error {
	if store.values == nil {
		store.values = map[string]string{}
	}
	store.values[key] = value
	return nil
}
func (store *templateStore) Delete(_ context.Context, key string) error {
	delete(store.values, key)
	return nil
}

func TestServiceStoresReadsAndResetsOverrides(t *testing.T) {
	store := &templateStore{}
	service := New(store)
	custom := "Plan {{issue_title}} from {{issue_body}}"
	updated, err := service.Update(t.Context(), IssueAgentPlanKey, custom)
	if err != nil || !updated.Overridden || service.Read(t.Context(), IssueAgentPlanKey) != custom {
		t.Fatalf("updated=%+v read=%q err=%v", updated, service.Read(t.Context(), IssueAgentPlanKey), err)
	}
	definition, _ := DefinitionByKey(IssueAgentPlanKey)
	reset, err := service.Update(t.Context(), IssueAgentPlanKey, definition.DefaultValue)
	if err != nil || reset.Overridden || service.Read(t.Context(), IssueAgentPlanKey) != definition.DefaultValue {
		t.Fatalf("reset=%+v read=%q err=%v", reset, service.Read(t.Context(), IssueAgentPlanKey), err)
	}
}
