package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type customFieldService struct {
	fieldRepo    repository.CustomFieldDefinitionRepository
	activityRepo repository.ActivityLogRepository
}

// NewCustomFieldService returns a new CustomFieldService backed by the given repositories.
func NewCustomFieldService(
	fieldRepo repository.CustomFieldDefinitionRepository,
	activityRepo repository.ActivityLogRepository,
) CustomFieldService {
	return &customFieldService{
		fieldRepo:    fieldRepo,
		activityRepo: activityRepo,
	}
}

// Create validates and persists a new custom field definition.
// It generates a slug from the name and assigns the next available position.
func (s *customFieldService) Create(ctx context.Context, field *domain.CustomFieldDefinition) error {
	if strings.TrimSpace(field.Name) == "" {
		return apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}

	if err := validateFieldOptions(field.FieldType, field.Options); err != nil {
		return err
	}

	if field.Slug == "" {
		field.Slug = slugify(field.Name)
	}

	if field.ID == uuid.Nil {
		field.ID = uuid.New()
	}

	// Assign the next position by inspecting existing fields in the project.
	existing, err := s.fieldRepo.ListByProject(ctx, field.ProjectID)
	if err != nil {
		return err
	}
	maxPos := -1
	for _, ef := range existing {
		if ef.Position > maxPos {
			maxPos = ef.Position
		}
	}
	field.Position = maxPos + 1

	field.CreatedAt = timeNow()

	return s.fieldRepo.Create(ctx, field)
}

// GetByID returns a custom field definition by ID.
func (s *customFieldService) GetByID(ctx context.Context, id uuid.UUID) (*domain.CustomFieldDefinition, error) {
	field, err := s.fieldRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if field == nil {
		return nil, apierror.NotFound("CustomFieldDefinition")
	}
	return field, nil
}

// Update validates that the field exists and persists changes.
func (s *customFieldService) Update(ctx context.Context, field *domain.CustomFieldDefinition) error {
	existing, err := s.fieldRepo.GetByID(ctx, field.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("CustomFieldDefinition")
	}

	if err := validateFieldOptions(field.FieldType, field.Options); err != nil {
		return err
	}

	return s.fieldRepo.Update(ctx, field)
}

// Delete removes a custom field definition after verifying it exists.
func (s *customFieldService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.fieldRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("CustomFieldDefinition")
	}
	return s.fieldRepo.Delete(ctx, id)
}

// ListByProject returns all custom field definitions for the given project.
func (s *customFieldService) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	return s.fieldRepo.ListByProject(ctx, projectID)
}

// ListVisibleToAgents returns only the custom field definitions visible to agents.
func (s *customFieldService) ListVisibleToAgents(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	return s.fieldRepo.ListVisibleToAgents(ctx, projectID)
}

// Reorder updates the position of each custom field definition in the given order.
func (s *customFieldService) Reorder(ctx context.Context, projectID uuid.UUID, fieldIDs []uuid.UUID) error {
	return s.fieldRepo.Reorder(ctx, projectID, fieldIDs)
}

// validateFieldOptions checks that the Options JSON is valid for the given field type.
func validateFieldOptions(ft domain.FieldType, opts json.RawMessage) error {
	if len(opts) == 0 || string(opts) == "{}" || string(opts) == "null" {
		// No options provided; only select/multiselect require them.
		if ft == domain.FieldTypeSelect || ft == domain.FieldTypeMultiselect {
			return apierror.ValidationError(map[string]string{
				"options": "choices are required for select/multiselect fields",
			})
		}
		return nil
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(opts, &parsed); err != nil {
		return apierror.ValidationError(map[string]string{
			"options": "options must be a valid JSON object",
		})
	}

	switch ft {
	case domain.FieldTypeSelect, domain.FieldTypeMultiselect:
		return validateSelectOptions(parsed)
	case domain.FieldTypeNumber:
		return validateNumberOptions(parsed)
	case domain.FieldTypeText:
		return validateTextOptions(parsed)
	case domain.FieldTypeDate, domain.FieldTypeDatetime:
		return validateDateOptions(parsed)
	default:
		// Other field types accept any options without specific validation.
		return nil
	}
}

// validateSelectOptions ensures select/multiselect options contain a valid "choices" array.
func validateSelectOptions(parsed map[string]json.RawMessage) error {
	choicesRaw, ok := parsed["choices"]
	if !ok {
		return apierror.ValidationError(map[string]string{
			"options": "choices are required for select/multiselect fields",
		})
	}

	var choices []string
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		return apierror.ValidationError(map[string]string{
			"options": "choices must be an array of strings",
		})
	}

	if len(choices) == 0 {
		return apierror.ValidationError(map[string]string{
			"options": "choices must contain at least one option",
		})
	}

	return nil
}

// validateNumberOptions ensures number options contain valid min/max values.
func validateNumberOptions(parsed map[string]json.RawMessage) error {
	var minVal, maxVal *float64

	if raw, ok := parsed["min"]; ok {
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return apierror.ValidationError(map[string]string{
				"options": "min must be a number",
			})
		}
		minVal = &v
	}

	if raw, ok := parsed["max"]; ok {
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return apierror.ValidationError(map[string]string{
				"options": "max must be a number",
			})
		}
		maxVal = &v
	}

	if minVal != nil && maxVal != nil && *minVal > *maxVal {
		return apierror.ValidationError(map[string]string{
			"options": "min must be less than or equal to max",
		})
	}

	return nil
}

// validateTextOptions ensures text options contain valid regex and max_length values.
func validateTextOptions(parsed map[string]json.RawMessage) error {
	if raw, ok := parsed["regex"]; ok {
		var pattern string
		if err := json.Unmarshal(raw, &pattern); err != nil {
			return apierror.ValidationError(map[string]string{
				"options": "regex must be a string",
			})
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return apierror.ValidationError(map[string]string{
				"options": "regex is not a valid regular expression",
			})
		}
	}

	if raw, ok := parsed["max_length"]; ok {
		var maxLen int
		if err := json.Unmarshal(raw, &maxLen); err != nil {
			return apierror.ValidationError(map[string]string{
				"options": "max_length must be an integer",
			})
		}
		if maxLen <= 0 {
			return apierror.ValidationError(map[string]string{
				"options": "max_length must be greater than 0",
			})
		}
	}

	return nil
}

// validateDateOptions ensures date/datetime options contain valid min_date/max_date values.
func validateDateOptions(parsed map[string]json.RawMessage) error {
	if raw, ok := parsed["min_date"]; ok {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return apierror.ValidationError(map[string]string{
				"options": "min_date must be a string",
			})
		}
	}

	if raw, ok := parsed["max_date"]; ok {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return apierror.ValidationError(map[string]string{
				"options": "max_date must be a string",
			})
		}
	}

	return nil
}

// ValidateValues validates custom field values against their definitions for a project.
// When isCreate is true, required fields that are missing from values produce validation errors.
func (s *customFieldService) ValidateValues(ctx context.Context, projectID uuid.UUID, values map[string]interface{}, isCreate bool) error {
	fields, err := s.fieldRepo.ListByProject(ctx, projectID)
	if err != nil {
		return err
	}

	// Build a map of slug -> definition for fast lookup.
	defBySlug := make(map[string]*domain.CustomFieldDefinition, len(fields))
	for i := range fields {
		defBySlug[fields[i].Slug] = &fields[i]
	}

	errs := make(map[string]string)

	// Validate each provided value against its definition.
	for slug, val := range values {
		def, ok := defBySlug[slug]
		if !ok {
			errs[slug] = fmt.Sprintf("unknown custom field %q", slug)
			continue
		}
		if valErr := validateFieldValue(def, val); valErr != "" {
			errs[slug] = valErr
		}
	}

	// Check required fields on create.
	if isCreate {
		for _, def := range fields {
			if def.IsRequired {
				if _, provided := values[def.Slug]; !provided {
					errs[def.Slug] = fmt.Sprintf("field %q is required", def.Slug)
				}
			}
		}
	}

	if len(errs) > 0 {
		return apierror.ValidationError(errs)
	}
	return nil
}

// validateFieldValue checks a single value against a field definition.
// Returns an error message string, or empty string if valid.
func validateFieldValue(def *domain.CustomFieldDefinition, val interface{}) string {
	if val == nil {
		return ""
	}

	switch def.FieldType {
	case domain.FieldTypeText:
		return validateTextValue(def, val)
	case domain.FieldTypeNumber:
		return validateNumberValue(def, val)
	case domain.FieldTypeDate:
		s, ok := val.(string)
		if !ok {
			return "must be a date string (YYYY-MM-DD)"
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return "must be a valid date in YYYY-MM-DD format"
		}
	case domain.FieldTypeDatetime:
		s, ok := val.(string)
		if !ok {
			return "must be a datetime string (RFC3339)"
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			return "must be a valid datetime in RFC3339 format"
		}
	case domain.FieldTypeSelect:
		return validateSelectValue(def, val)
	case domain.FieldTypeMultiselect:
		return validateMultiselectValue(def, val)
	case domain.FieldTypeURL:
		s, ok := val.(string)
		if !ok {
			return "must be a string URL"
		}
		if _, err := url.ParseRequestURI(s); err != nil {
			return "must be a valid URL"
		}
	case domain.FieldTypeEmail:
		s, ok := val.(string)
		if !ok {
			return "must be a string email"
		}
		if !strings.Contains(s, "@") {
			return "must be a valid email address"
		}
	case domain.FieldTypeCheckbox:
		if _, ok := val.(bool); !ok {
			return "must be a boolean"
		}
	case domain.FieldTypeUserRef, domain.FieldTypeAgentRef:
		s, ok := val.(string)
		if !ok {
			return "must be a UUID string"
		}
		if _, err := uuid.Parse(s); err != nil {
			return "must be a valid UUID"
		}
	case domain.FieldTypeJSON:
		// Attempt a round-trip marshal/unmarshal to validate JSON.
		b, err := json.Marshal(val)
		if err != nil {
			return "must be valid JSON"
		}
		var tmp interface{}
		if err := json.Unmarshal(b, &tmp); err != nil {
			return "must be valid JSON"
		}
	}
	return ""
}

// validateTextValue checks a text field value against its options (max_length).
func validateTextValue(def *domain.CustomFieldDefinition, val interface{}) string {
	s, ok := val.(string)
	if !ok {
		return "must be a string"
	}
	if len(def.Options) > 0 && string(def.Options) != "{}" && string(def.Options) != "null" {
		var opts map[string]json.RawMessage
		if err := json.Unmarshal(def.Options, &opts); err == nil {
			if raw, ok := opts["max_length"]; ok {
				var maxLen int
				if err := json.Unmarshal(raw, &maxLen); err == nil && maxLen > 0 {
					if len(s) > maxLen {
						return fmt.Sprintf("exceeds max length of %d", maxLen)
					}
				}
			}
		}
	}
	return ""
}

// validateNumberValue checks a number field value against its options (min, max).
func validateNumberValue(def *domain.CustomFieldDefinition, val interface{}) string {
	var num float64
	switch v := val.(type) {
	case float64:
		num = v
	case int:
		num = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return "must be a number"
		}
		num = f
	default:
		return "must be a number"
	}
	if len(def.Options) > 0 && string(def.Options) != "{}" && string(def.Options) != "null" {
		var opts map[string]json.RawMessage
		if err := json.Unmarshal(def.Options, &opts); err == nil {
			if raw, ok := opts["min"]; ok {
				var minVal float64
				if err := json.Unmarshal(raw, &minVal); err == nil {
					if num < minVal {
						return fmt.Sprintf("must be >= %v", minVal)
					}
				}
			}
			if raw, ok := opts["max"]; ok {
				var maxVal float64
				if err := json.Unmarshal(raw, &maxVal); err == nil {
					if num > maxVal {
						return fmt.Sprintf("must be <= %v", maxVal)
					}
				}
			}
		}
	}
	return ""
}

// validateSelectValue checks a select field value against its choices.
func validateSelectValue(def *domain.CustomFieldDefinition, val interface{}) string {
	s, ok := val.(string)
	if !ok {
		return "must be a string"
	}
	choices := extractChoices(def.Options)
	if len(choices) > 0 {
		for _, c := range choices {
			if c == s {
				return ""
			}
		}
		return fmt.Sprintf("must be one of: %s", strings.Join(choices, ", "))
	}
	return ""
}

// validateMultiselectValue checks a multiselect field value against its choices.
func validateMultiselectValue(def *domain.CustomFieldDefinition, val interface{}) string {
	arr, ok := val.([]interface{})
	if !ok {
		return "must be an array of strings"
	}
	choices := extractChoices(def.Options)
	choiceSet := make(map[string]bool, len(choices))
	for _, c := range choices {
		choiceSet[c] = true
	}
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return "all items must be strings"
		}
		if len(choices) > 0 && !choiceSet[s] {
			return fmt.Sprintf("%q is not a valid choice", s)
		}
	}
	return ""
}

// extractChoices extracts the "choices" string array from field options JSON.
func extractChoices(opts json.RawMessage) []string {
	if len(opts) == 0 || string(opts) == "{}" || string(opts) == "null" {
		return nil
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(opts, &parsed); err != nil {
		return nil
	}
	choicesRaw, ok := parsed["choices"]
	if !ok {
		return nil
	}
	var choices []string
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		return nil
	}
	return choices
}
