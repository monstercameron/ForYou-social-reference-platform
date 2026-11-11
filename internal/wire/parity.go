package wire

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// parityRegistry maps object definitions in the schemas to their Go types.
var parityRegistry = map[string]reflect.Type{
	"Version":                         reflect.TypeOf(Version{}),
	"SubmissionReceipt":               reflect.TypeOf(SubmissionReceipt{}),
	"AuthorizationEpoch":              reflect.TypeOf(AuthorizationEpoch{}),
	"AuthorizationEpochState":         reflect.TypeOf(AuthorizationEpochState{}),
	"AuthorizationEpochStateResponse": reflect.TypeOf(AuthorizationEpochStateResponse{}),
	"AuthorizationEpochEnroll":        reflect.TypeOf(AuthorizationEpochEnroll{}),
	"AuthorizationEpochControl":       reflect.TypeOf(AuthorizationEpochControl{}),
	"Delegation":                      reflect.TypeOf(Delegation{}),
	"EncryptedStateRecipient":         reflect.TypeOf(EncryptedStateRecipient{}),
	"EncryptedStateEnvelope":          reflect.TypeOf(EncryptedStateEnvelope{}),
	"PostPayload":                     reflect.TypeOf(PostPayload{}),
	"MessageBase":                     reflect.TypeOf(MessageBase{}),
	"PostMessage":                     reflect.TypeOf(PostMessage{}),
	"TransportMessage":                reflect.TypeOf(TransportMessage{}),
	"RevocationRequest":               reflect.TypeOf(RevocationRequest{}),
	"AdmissionAuthorization":          reflect.TypeOf(AdmissionAuthorization{}),
	"AdmissionRequest":                reflect.TypeOf(AdmissionRequest{}),
	"MessageAcceptance":               reflect.TypeOf(MessageAcceptance{}),
	"PostReference":                   reflect.TypeOf(PostReference{}),
	"MediaReference":                  reflect.TypeOf(MediaReference{}),
	"PreparedAdmissionRecord":         reflect.TypeOf(PreparedAdmissionRecord{}),
	"PublicTransportMessage":          reflect.TypeOf(PublicTransportMessage{}),
	"DiscoveryPage":                   reflect.TypeOf(DiscoveryPage{}),
	"ArchivePayload":                  reflect.TypeOf(ArchivePayload{}),
	"ArchiveRecord":                   reflect.TypeOf(ArchiveRecord{}),
	"MessageListQuery":                reflect.TypeOf(MessageListQuery{}),
	"AuthorMessageListQuery":          reflect.TypeOf(AuthorMessageListQuery{}),
	"KeySuccession":                   reflect.TypeOf(KeySuccession{}),
	"ChangesPage":                     reflect.TypeOf(ChangesPage{}),
	"IntakeForm":                      reflect.TypeOf(IntakeForm{}),
	"IntakeRequest":                   reflect.TypeOf(IntakeRequest{}),
	"IntakeReceipt":                   reflect.TypeOf(IntakeReceipt{}),
	"IntakeRecovery":                  reflect.TypeOf(IntakeRecovery{}),
	"OperatorManifest":                reflect.TypeOf(OperatorManifest{}),
	"ExportEntry":                     reflect.TypeOf(ExportEntry{}),
	"ServiceChallenge":                reflect.TypeOf(ServiceChallenge{}),
	"ExportRequest":                   reflect.TypeOf(ExportRequest{}),
	"ExportPartManifest":              reflect.TypeOf(ExportPartManifest{}),
	"ExportPartDescriptor":            reflect.TypeOf(ExportPartDescriptor{}),
	"ExportSetIndex":                  reflect.TypeOf(ExportSetIndex{}),
	"ExportSetManifest":               reflect.TypeOf(ExportSetManifest{}),
	"ExportJobStatus":                 reflect.TypeOf(ExportJobStatus{}),
	"ExportJobCreated":                reflect.TypeOf(ExportJobCreated{}),
	"ProviderServiceEvidence":         reflect.TypeOf(ProviderServiceEvidence{}),
	"ServiceEvidence":                 reflect.TypeOf(ServiceEvidence{}),
	"FeeSchedule":                     reflect.TypeOf(FeeSchedule{}),
	"FeePreflightRequest":             reflect.TypeOf(FeePreflightRequest{}),
	"FeeQuote":                        reflect.TypeOf(FeeQuote{}),
	"DepositRequest":                  reflect.TypeOf(DepositRequest{}),
	"DepositInvoice":                  reflect.TypeOf(DepositInvoice{}),
	"DepositTransfer":                 reflect.TypeOf(DepositTransfer{}),
	"DepositProof":                    reflect.TypeOf(DepositProof{}),
	"DepositCredit":                   reflect.TypeOf(DepositCredit{}),
	"BalanceStatus":                   reflect.TypeOf(BalanceStatus{}),
	"QualityReport":                   reflect.TypeOf(QualityReport{}),
	"QualityObservationSet":           reflect.TypeOf(QualityObservationSet{}),
	"FeeScheduleApproval":             reflect.TypeOf(FeeScheduleApproval{}),
	"SettlementLeaf":                  reflect.TypeOf(SettlementLeaf{}),
	"SettlementClaim":                 reflect.TypeOf(SettlementClaim{}),
	"SettlementProof":                 reflect.TypeOf(SettlementProof{}),
	"DistributionPolicy":              reflect.TypeOf(DistributionPolicy{}),
	"DistributionStatement":           reflect.TypeOf(DistributionStatement{}),
	"RetentionDeclaration":            reflect.TypeOf(RetentionDeclaration{}),
	"SettlementWitness":               reflect.TypeOf(SettlementWitness{}),
	"FoundationKeyCertificate":        reflect.TypeOf(FoundationKeyCertificate{}),
	"VaultStatus":                     reflect.TypeOf(VaultStatus{}),
	"FoundationKeyClosure":            reflect.TypeOf(FoundationKeyClosure{}),
	"FoundationLogEntry":              reflect.TypeOf(FoundationLogEntry{}),
	"VaultReceipt":                    reflect.TypeOf(VaultReceipt{}),
	"GenesisManifest":                 reflect.TypeOf(GenesisManifest{}),
	"WithdrawalRequest":               reflect.TypeOf(WithdrawalRequest{}),
	"WithdrawalStatus":                reflect.TypeOf(WithdrawalStatus{}),
	"FoundationLogHead":               reflect.TypeOf(FoundationLogHead{}),
	"FoundationLogProof":              reflect.TypeOf(FoundationLogProof{}),
	"FoundationLogPage":               reflect.TypeOf(FoundationLogPage{}),
	"SettlementClaimsPage":            reflect.TypeOf(SettlementClaimsPage{}),
	"SettlementLeavesPage":            reflect.TypeOf(SettlementLeavesPage{}),
	"RetentionDeclarationsPage":       reflect.TypeOf(RetentionDeclarationsPage{}),
	"ReleaseKeySetUpdate":             reflect.TypeOf(ReleaseKeySetUpdate{}),
	"FoundationCertificatesPage":      reflect.TypeOf(FoundationCertificatesPage{}),
	"FeeSchedulesPage":                reflect.TypeOf(FeeSchedulesPage{}),
	"DistributionPage":                reflect.TypeOf(DistributionPage{}),
	"VaultStatusPage":                 reflect.TypeOf(VaultStatusPage{}),
	"QualityReportsPage":              reflect.TypeOf(QualityReportsPage{}),
	"SettlementWitnessesPage":         reflect.TypeOf(SettlementWitnessesPage{}),
	"SettlementRefusal":               reflect.TypeOf(SettlementRefusal{}),
	"MonitorWitnessesPage":            reflect.TypeOf(MonitorWitnessesPage{}),
	"EquivocationsPage":               reflect.TypeOf(EquivocationsPage{}),
	"EquivocationProof":               reflect.TypeOf(EquivocationProof{}),
	"VaultDelivery":                   reflect.TypeOf(VaultDelivery{}),
	"FoundationLogHeadsPage":          reflect.TypeOf(FoundationLogHeadsPage{}),
	"SettlementHold":                  reflect.TypeOf(SettlementHold{}),
}

// NameRecord and NamesPage belong to internal/names, the optional display names profile.
var parityExemptDefinitions = map[string]bool{"NameRecord": true, "NamesPage": true}

// parityMismatches reports structural differences between a Go object and its schema definition.
func parityMismatches(def string, typ reflect.Type) []string {
	mismatches := parityDefinitionMismatches(def, typ)
	sort.Strings(mismatches)
	return mismatches
}

func parityDefinitionMismatches(def string, typ reflect.Type) []string {
	schema := parityFindDefinition(def)
	if schema == nil || schemaState.err != nil {
		return []string{def + ": schema definition unavailable"}
	}
	return parityCompareObject(def, typ, schema)
}

func parityRegistryMismatches() []string {
	loadSchemas()
	var mismatches []string
	registered := make(map[string]bool, len(parityRegistry))
	for _, doc := range []schemaDocument{schemaState.launch, schemaState.operations} {
		for name, schema := range doc.defs {
			if !parityIsObject(schema) || name == "PublicReadError" || parityExemptDefinitions[name] {
				continue
			}
			if _, ok := parityRegistry[name]; !ok {
				mismatches = append(mismatches, name+": object definition is not registered")
			} else {
				registered[name] = true
			}
		}
	}
	for name := range parityRegistry {
		if !registered[name] {
			mismatches = append(mismatches, name+": registry entry is not a non-exempt object definition")
		}
	}
	sort.Strings(mismatches)
	return mismatches
}

func paritySchemaMismatches(name string) []string {
	loadSchemas()
	var doc schemaDocument
	switch name {
	case "launch":
		doc = schemaState.launch
	case "operations":
		doc = schemaState.operations
	default:
		return []string{"unknown schema " + name}
	}
	var mismatches []string
	for def, schema := range doc.defs {
		if !parityIsObject(schema) || def == "PublicReadError" || parityExemptDefinitions[def] {
			continue
		}
		if typ, ok := parityRegistry[def]; ok {
			mismatches = append(mismatches, parityMismatches(def, typ)...)
		}
	}
	sort.Strings(mismatches)
	return mismatches
}

func parityIsObject(schema any) bool {
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if object["type"] == "object" || object["properties"] != nil || object["allOf"] != nil || object["oneOf"] != nil {
		return true
	}
	if ref, ok := object["$ref"].(string); ok {
		return parityIsObject(parityFindDefinition(strings.TrimPrefix(ref, "#/$defs/")))
	}
	return false
}

func parityCompareObject(def string, typ reflect.Type, schema any) []string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	properties, required, excluded := parityObjectMembers(schema)
	if typ.Kind() != reflect.Struct {
		return []string{def + ": Go type is not a struct"}
	}
	var mismatches []string
	fields := make(map[string]reflect.StructField, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		key := strings.Split(field.Tag.Get("json"), ",")[0]
		if key == "" || key == "-" {
			continue
		}
		fields[key] = field
	}
	for key, field := range fields {
		if _, exists := properties[key]; !exists || excluded[key] {
			mismatches = append(mismatches, def+"."+key+": unexpected Go field "+field.Name)
		}
	}
	for key, property := range properties {
		field, exists := fields[key]
		if !exists || excluded[key] {
			if !exists && !excluded[key] {
				mismatches = append(mismatches, def+"."+key+": missing Go field")
			}
			continue
		}
		tagParts := strings.Split(field.Tag.Get("json"), ",")
		optional := false
		for _, part := range tagParts[1:] {
			if part == "omitempty" {
				optional = true
			}
		}
		_, isRequired := required[key]
		nullable := parityNullable(property)
		if optional == isRequired {
			mismatches = append(mismatches, def+"."+key+": field "+field.Name+" omitempty disagrees with required")
		}
		fieldType := field.Type
		isPointer := fieldType.Kind() == reflect.Pointer
		if isPointer {
			fieldType = fieldType.Elem()
		}
		if isPointer != (nullable || (!isRequired && parityZeroValueValid(property))) {
			mismatches = append(mismatches, def+"."+key+": field "+field.Name+" pointer disagrees with nullability")
		}
		mismatches = append(mismatches, parityCompareValue(def, key, fieldType, property)...)
	}
	return mismatches
}

func parityObjectMembers(schema any) (map[string]any, map[string]struct{}, map[string]bool) {
	properties := map[string]any{}
	required := map[string]struct{}{}
	excluded := map[string]bool{}
	var visit func(any)
	visit = func(value any) {
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		if ref, ok := object["$ref"].(string); ok {
			const prefix = "#/$defs/"
			if strings.HasPrefix(ref, prefix) {
				visit(parityFindDefinition(strings.TrimPrefix(ref, prefix)))
			}
		}
		if choices, ok := object["oneOf"].([]any); ok {
			var sharedRequired map[string]struct{}
			for _, choice := range choices {
				branchProperties, branchRequired, branchExcluded := parityObjectMembers(choice)
				for key, property := range branchProperties {
					if _, exists := properties[key]; !exists {
						properties[key] = property
					}
				}
				for key := range branchExcluded {
					excluded[key] = true
				}
				if sharedRequired == nil {
					sharedRequired = branchRequired
				} else {
					for key := range sharedRequired {
						if _, exists := branchRequired[key]; !exists {
							delete(sharedRequired, key)
						}
					}
				}
			}
			for key := range sharedRequired {
				required[key] = struct{}{}
			}
		}
		if props, ok := object["properties"].(map[string]any); ok {
			for key, prop := range props {
				properties[key] = prop
			}
		}
		if names, ok := object["required"].([]any); ok {
			for _, name := range names {
				if value, ok := name.(string); ok {
					required[value] = struct{}{}
				}
			}
		}
		if all, ok := object["allOf"].([]any); ok {
			for _, branch := range all {
				visit(branch)
			}
		}
		if names, ok := object["propertyNames"].(map[string]any); ok {
			if enum, ok := names["enum"].([]any); ok {
				allowed := make(map[string]bool, len(enum))
				for _, value := range enum {
					if key, ok := value.(string); ok {
						allowed[key] = true
					}
				}
				for key := range properties {
					if !allowed[key] {
						excluded[key] = true
					}
				}
			}
		}
	}
	visit(schema)
	return properties, required, excluded
}

func parityFindDefinition(name string) any {
	loadSchemas()
	if schema, ok := schemaState.launch.defs[name]; ok {
		return schema
	}
	return schemaState.operations.defs[name]
}

func parityNullable(schema any) bool {
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if types, ok := object["type"].([]any); ok {
		for _, value := range types {
			if value == "null" {
				return true
			}
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if branches, ok := object[keyword].([]any); ok {
			for _, branch := range branches {
				if branchObject, ok := branch.(map[string]any); ok && branchObject["type"] == "null" {
					return true
				}
			}
		}
	}
	return false
}

func parityZeroValueValid(schema any) bool {
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if types, ok := object["type"].([]any); ok {
		for _, value := range types {
			if value == "null" {
				return true
			}
		}
	}
	if ref, ok := object["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		target := parityFindDefinition(name)
		return parityIsObject(target)
	}
	switch object["type"] {
	case "array", "object", "boolean":
		if object["type"] == "array" {
			if minimum, ok := object["minItems"].(json.Number); ok && minimum != "0" {
				return false
			}
		}
		return true
	case "string":
		if minimum, ok := object["minLength"].(json.Number); ok && minimum != "0" {
			return false
		}
		return object["const"] == nil && object["enum"] == nil && object["pattern"] == nil
	case "integer", "number":
		minimum, hasMinimum := object["minimum"].(json.Number)
		maximum, hasMaximum := object["maximum"].(json.Number)
		minimumValue, _ := strconv.ParseFloat(string(minimum), 64)
		maximumValue, _ := strconv.ParseFloat(string(maximum), 64)
		return (!hasMinimum || minimumValue <= 0) && (!hasMaximum || maximumValue >= 0)
	}
	return false
}

func parityCompareValue(def, property string, typ reflect.Type, schema any) []string {
	object, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	if ref, ok := object["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		target := parityFindDefinition(name)
		resolved := target
		for {
			refObject, ok := resolved.(map[string]any)
			if !ok {
				break
			}
			nextRef, ok := refObject["$ref"].(string)
			if !ok {
				break
			}
			nextName := strings.TrimPrefix(nextRef, "#/$defs/")
			resolved = parityFindDefinition(nextName)
		}
		if parityIsObject(resolved) {
			want := parityRegistry[name]
			if want != nil && typ != want {
				return []string{def + "." + property + ": Go field has type " + typ.String() + ", want " + want.String() + " for $ref " + name}
			}
			return parityCompareObject(def+"."+property, typ, resolved)
		}
		if typ.Kind() != reflect.String {
			return []string{def + "." + property + ": Go field has type " + typ.String() + ", want string for $ref " + name}
		}
		return nil
	}
	if items, ok := object["items"]; ok && typ.Kind() == reflect.Slice {
		return parityCompareValue(def, property+"[]", typ.Elem(), items)
	}
	if rawProperties, ok := object["properties"].(map[string]any); ok && typ.Kind() == reflect.Struct {
		return parityCompareObject(def+"."+property, typ, map[string]any{"properties": rawProperties, "required": object["required"]})
	}
	if types, ok := object["type"].([]any); ok {
		for _, candidate := range types {
			if candidate != "null" {
				return parityComparePrimitive(def, property, typ, candidate)
			}
		}
	}
	if kind, ok := object["type"].(string); ok {
		return parityComparePrimitive(def, property, typ, kind)
	}
	return nil
}

func parityComparePrimitive(def, property string, typ reflect.Type, schemaType any) []string {
	want := map[any]reflect.Kind{"string": reflect.String, "integer": reflect.Int64, "boolean": reflect.Bool, "array": reflect.Slice, "object": reflect.Struct}[schemaType]
	if schemaType == "integer" && (typ.Kind() == reflect.Int || typ.Kind() == reflect.Int64) {
		return nil
	}
	if schemaType == "object" && (typ == reflect.TypeOf(map[string]string{}) || typ == reflect.TypeOf(json.RawMessage{})) {
		return nil
	}
	if want != reflect.Invalid && typ.Kind() != want {
		return []string{def + "." + property + ": Go field has type " + typ.String() + ", incompatible with schema type " + schemaType.(string)}
	}
	return nil
}
