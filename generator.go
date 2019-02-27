package main

import (
	"fmt"
	"strings"

	"github.com/golang/protobuf/proto"
	pb "github.com/golang/protobuf/protoc-gen-go/descriptor"
	"github.com/golang/protobuf/protoc-gen-go/generator"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"

	"github.com/martinxsliu/protoc-gen-graphql/descriptor"
	"github.com/martinxsliu/protoc-gen-graphql/graphql"
	"github.com/martinxsliu/protoc-gen-graphql/graphqlpb"
)

const header = `# DO NOT EDIT! Generated by protoc-gen-graphql.`

type Generator struct {
	req    *plugin.CodeGeneratorRequest
	resp   *plugin.CodeGeneratorResponse
	params *Parameters

	files    map[string]*descriptor.FileDescriptor
	messages map[string]*descriptor.MessageDescriptor
	enums    map[string]*descriptor.EnumDescriptor

	emptyMessages map[string]bool

	// Maps qualified protobuf types to graphql types.
	// e.g. google.protobuf.StringValue -> GoogleProtobuf_StringValue
	typeNameMap  map[string]string
	inputNameMap map[string]string
}

func New(req *plugin.CodeGeneratorRequest, resp *plugin.CodeGeneratorResponse) (*Generator, error) {
	params, err := NewParameters(req.GetParameter())
	if err != nil {
		return nil, err
	}

	return &Generator{
		req:    req,
		resp:   resp,
		params: params,
	}, nil
}

func (g *Generator) Generate() error {
	g.wrapFiles()
	g.generateFiles()
	return nil
}

func (g *Generator) wrapFiles() {
	g.files = make(map[string]*descriptor.FileDescriptor)
	for _, fileProto := range g.req.GetProtoFile() {
		g.files[fileProto.GetName()] = descriptor.WrapFile(fileProto)
	}
	g.buildTypeMaps()
}

func (g *Generator) buildTypeMaps() {
	g.messages = make(map[string]*descriptor.MessageDescriptor)
	g.enums = make(map[string]*descriptor.EnumDescriptor)
	g.emptyMessages = make(map[string]bool)
	g.typeNameMap = make(map[string]string)
	g.inputNameMap = make(map[string]string)

	// Ensure that we're iterating in topological order.
	for _, fileDescriptor := range g.req.GetProtoFile() {
		file := g.files[fileDescriptor.GetName()]

		for _, message := range file.Messages {
			g.messages[message.FullName] = message
		}
		for _, enum := range file.Enums {
			g.enums[enum.FullName] = enum
		}

		if !g.params.ServiceTypesOnly {
			// Build the message and enum map for the file first, before building the
			// proto to graphql name maps.
			for _, message := range file.Messages {
				g.buildTypesFromMessage(message, false)
			}
			for _, enum := range file.Enums {
				g.buildTypesFromEnum(enum)
			}
		}

		for _, service := range file.Services {
			g.typeNameMap[service.FullName] = buildGraphqlTypeName(&graphqlTypeNameParts{
				Package:  file.Proto.GetPackage(),
				TypeName: service.TypeName,
			})

			for _, method := range service.Proto.GetMethod() {
				if g.params.ServiceTypesOnly {
					g.buildTypesFromMessage(g.messages[method.GetOutputType()], false)
				}
				g.buildTypesFromMessage(g.messages[method.GetInputType()], true)
			}
		}
	}
}

func (g *Generator) buildTypesFromMessage(message *descriptor.MessageDescriptor, input bool) {
	nameMap := g.typeNameMap
	if input {
		nameMap = g.inputNameMap
	}

	if nameMap[message.FullName] != "" {
		return
	}
	if len(message.Proto.GetField()) == 0 {
		g.emptyMessages[message.FullName] = true
		return
	}

	nameMap[message.FullName] = buildGraphqlTypeName(&graphqlTypeNameParts{
		Package:    message.Package,
		TypeName:   message.TypeName,
		Input:      input,
		IsProtoMap: message.IsMap,
	})

	for _, field := range message.Proto.GetField() {
		if field.GetType() == pb.FieldDescriptorProto_TYPE_MESSAGE {
			g.buildTypesFromMessage(g.messages[field.GetTypeName()], input)
		}

		if field.GetType() == pb.FieldDescriptorProto_TYPE_ENUM {
			g.buildTypesFromEnum(g.enums[field.GetTypeName()])
		}
	}
}

func (g *Generator) buildTypesFromEnum(enum *descriptor.EnumDescriptor) {
	g.typeNameMap[enum.FullName] = buildGraphqlTypeName(&graphqlTypeNameParts{
		Package:  enum.Package,
		TypeName: enum.TypeName,
	})
}

func (g *Generator) generateFiles() {
	for _, fileName := range g.req.GetFileToGenerate() {
		fileResp := &plugin.CodeGeneratorResponse_File{}
		fileResp.Name = stringPtr(graphqlFileName(fileName))

		file := g.files[fileName]

		var gqlTypes []graphql.Type

		for _, service := range file.Services {
			for _, gqlType := range g.graphqlFromService(service) {
				gqlTypes = append(gqlTypes, gqlType)
			}
		}

		for _, message := range file.Messages {
			for _, gqlType := range g.graphqlFromMessage(message) {
				gqlTypes = append(gqlTypes, gqlType)
			}
		}

		for _, enum := range file.Enums {
			for _, gqlType := range g.graphqlFromEnum(enum) {
				gqlTypes = append(gqlTypes, gqlType)
			}
		}

		// Don't generate files without any type declarations.
		if len(gqlTypes) == 0 {
			continue
		}

		var b strings.Builder
		b.WriteString(header)
		for _, gqlType := range gqlTypes {
			b.WriteString("\n\n")
			b.WriteString(graphql.TypeDef(gqlType))
		}
		b.WriteString("\n")
		fileResp.Content = stringPtr(b.String())

		g.resp.File = append(g.resp.File, fileResp)
	}
}

func (g *Generator) graphqlFromMessage(message *descriptor.MessageDescriptor) []graphql.Type {
	var graphqlTypes []graphql.Type

	if typeName, ok := g.typeNameMap[message.FullName]; ok {
		graphqlTypes = append(graphqlTypes, &graphql.Object{
			Name:   typeName,
			Fields: g.graphqlFields(message, false),
		})

		for i := 0; i < len(message.Proto.GetOneofDecl()); i++ {
			graphqlTypes = append(graphqlTypes, g.graphqlUnionFromOneof(message, int32(i))...)
		}
	}

	if inputName, ok := g.inputNameMap[message.FullName]; ok {
		graphqlTypes = append(graphqlTypes, &graphql.Input{
			Name:   inputName,
			Fields: g.graphqlFields(message, true),
		})

		for i := 0; i < len(message.Proto.GetOneofDecl()); i++ {
			graphqlTypes = append(graphqlTypes, g.graphqlInputFromOneof(message, int32(i)))
		}
	}

	return graphqlTypes
}

func (g *Generator) graphqlFields(message *descriptor.MessageDescriptor, input bool) []*graphql.Field {
	var fields []*graphql.Field

	seenOneofs := make(map[int32]bool)
	for _, fieldProto := range message.Proto.GetField() {
		if fieldProto.OneofIndex == nil {
			// Handle normal field.
			fields = append(fields, g.graphqlField(fieldProto, fieldOptions{Input: input}))
			continue
		}

		// Handle field that's part of a oneof. We only want to append the graphql oneof type
		// the first time we encounter it.
		index := *fieldProto.OneofIndex
		if seenOneofs[index] {
			continue
		}

		oneof := message.Proto.GetOneofDecl()[index].GetName()
		fields = append(fields, &graphql.Field{
			Name: oneof,
			TypeName: buildGraphqlTypeName(&graphqlTypeNameParts{
				Package:  message.Package,
				TypeName: append(message.TypeName, oneof),
				Input:    input,
			}),
		})

		seenOneofs[index] = true
	}

	return fields
}

type fieldOptions struct {
	Input           bool
	NullableScalars bool
}

func (g *Generator) graphqlField(proto *pb.FieldDescriptorProto, options fieldOptions) *graphql.Field {
	field := &graphql.Field{
		Name: proto.GetName(),
	}

	switch proto.GetType() {
	case pb.FieldDescriptorProto_TYPE_FLOAT, pb.FieldDescriptorProto_TYPE_DOUBLE,
		pb.FieldDescriptorProto_TYPE_UINT32, pb.FieldDescriptorProto_TYPE_SINT32,
		pb.FieldDescriptorProto_TYPE_FIXED32, pb.FieldDescriptorProto_TYPE_SFIXED32:

		field.TypeName = graphql.ScalarFloat.TypeName()
		field.Modifiers = graphql.TypeModifierNonNull

	case pb.FieldDescriptorProto_TYPE_STRING, pb.FieldDescriptorProto_TYPE_BYTES,
		pb.FieldDescriptorProto_TYPE_INT64, pb.FieldDescriptorProto_TYPE_UINT64, pb.FieldDescriptorProto_TYPE_SINT64,
		pb.FieldDescriptorProto_TYPE_FIXED64, pb.FieldDescriptorProto_TYPE_SFIXED64:

		field.TypeName = graphql.ScalarString.TypeName()
		if !options.NullableScalars {
			field.Modifiers = graphql.TypeModifierNonNull
		}

	case pb.FieldDescriptorProto_TYPE_INT32:
		field.TypeName = graphql.ScalarInt.TypeName()
		if !options.NullableScalars {
			field.Modifiers = graphql.TypeModifierNonNull
		}

	case pb.FieldDescriptorProto_TYPE_BOOL:
		field.TypeName = graphql.ScalarBoolean.TypeName()
		if !options.NullableScalars {
			field.Modifiers = graphql.TypeModifierNonNull
		}

	case pb.FieldDescriptorProto_TYPE_ENUM:
		field.TypeName = g.typeNameMap[proto.GetTypeName()]
		if !options.NullableScalars {
			field.Modifiers = graphql.TypeModifierNonNull
		}

	case pb.FieldDescriptorProto_TYPE_MESSAGE:
		if g.emptyMessages[proto.GetTypeName()] {
			field.TypeName = graphql.ScalarBoolean.TypeName()
			break
		}

		if options.Input {
			field.TypeName = g.inputNameMap[proto.GetTypeName()]
		} else {
			field.TypeName = g.typeNameMap[proto.GetTypeName()]
		}

		// Map elements are non-nullable.
		if g.messages[proto.GetTypeName()].IsMap {
			field.Modifiers = graphql.TypeModifierNonNull
		}

	default:
		panic(fmt.Sprintf("unexpected protobuf descriptor type: %s", proto.GetType().String()))
	}

	if proto.GetLabel() == pb.FieldDescriptorProto_LABEL_REPEATED {
		field.Modifiers = field.Modifiers | graphql.TypeModifierList
	}

	return g.graphqlSpecialTypes(field, proto.GetTypeName())
}

func (g *Generator) graphqlSpecialTypes(field *graphql.Field, protoTypeName string) *graphql.Field {
	if protoTypeName == ".google.protobuf.Timestamp" && g.params.TimestampTypeName != "" {
		field.TypeName = g.params.TimestampTypeName
	}
	if protoTypeName == ".google.protobuf.Duration" && g.params.DurationTypeName != "" {
		field.TypeName = g.params.DurationTypeName
	}

	if g.params.WrappersAsNull {
		switch protoTypeName {
		case ".google.protobuf.FloatValue", ".google.protobuf.DoubleValue", ".google.protobuf.UInt32Value":
			field.TypeName = graphql.ScalarFloat.TypeName()
		case ".google.protobuf.StringValue", ".google.protobuf.BytesValue", ".google.protobuf.Int64Value", ".google.protobuf.UInt64Value":
			field.TypeName = graphql.ScalarString.TypeName()
		case ".google.protobuf.Int32Value":
			field.TypeName = graphql.ScalarInt.TypeName()
		case ".google.protobuf.BoolValue":
			field.TypeName = graphql.ScalarBoolean.TypeName()
		}
	}

	return field
}

func (g *Generator) graphqlUnionFromOneof(message *descriptor.MessageDescriptor, oneofIndex int32) []graphql.Type {
	oneof := message.Proto.GetOneofDecl()[oneofIndex].GetName()
	union := &graphql.Union{
		Name: buildGraphqlTypeName(&graphqlTypeNameParts{
			Package:  message.Package,
			TypeName: append(message.TypeName, oneof),
		}),
	}
	graphqlTypes := []graphql.Type{union}

	for _, fieldProto := range message.Proto.GetField() {
		if fieldProto.OneofIndex == nil || *fieldProto.OneofIndex != oneofIndex {
			continue
		}

		typeName := buildGraphqlTypeName(&graphqlTypeNameParts{
			Package:  message.Package,
			TypeName: append(message.TypeName, oneof, fieldProto.GetName()),
		})

		union.TypeNames = append(union.TypeNames, typeName)
		graphqlTypes = append(graphqlTypes, &graphql.Object{
			Name:   typeName,
			Fields: []*graphql.Field{g.graphqlField(fieldProto, fieldOptions{})},
		})
	}

	return graphqlTypes
}

func (g *Generator) graphqlInputFromOneof(message *descriptor.MessageDescriptor, oneofIndex int32) graphql.Type {
	oneof := message.Proto.GetOneofDecl()[oneofIndex].GetName()

	var fields []*graphql.Field
	for _, fieldProto := range message.Proto.GetField() {
		if fieldProto.OneofIndex == nil || *fieldProto.OneofIndex != oneofIndex {
			continue
		}
		fields = append(fields, g.graphqlField(fieldProto, fieldOptions{Input: true, NullableScalars: true}))
	}

	return &graphql.Input{
		Name: buildGraphqlTypeName(&graphqlTypeNameParts{
			Package:  message.Package,
			TypeName: append(message.TypeName, oneof),
			Input:    true,
		}),
		Fields: fields,
	}
}

func (g *Generator) graphqlFromEnum(enum *descriptor.EnumDescriptor) []graphql.Type {
	var graphqlTypes []graphql.Type

	if g.typeNameMap[enum.FullName] == "" {
		return graphqlTypes
	}

	var values []string
	for _, protoValue := range enum.Proto.GetValue() {
		values = append(values, protoValue.GetName())
	}

	graphqlTypes = append(graphqlTypes, &graphql.Enum{
		Name:   g.typeNameMap[enum.FullName],
		Values: values,
	})

	return graphqlTypes
}

func (g *Generator) graphqlFromService(service *descriptor.ServiceDescriptor) []graphql.Type {
	var (
		graphqlTypes  []graphql.Type
		queries       []*graphql.Field
		mutations     []*graphql.Field
		subscriptions []*graphql.Field
	)

	for _, method := range service.Proto.GetMethod() {
		var operation string
		if proto.HasExtension(method.GetOptions(), graphqlpb.E_Operation) {
			extVal, err := proto.GetExtension(method.GetOptions(), graphqlpb.E_Operation)
			if err != nil {
				panic(err)
			}
			operation = *extVal.(*string)
		}

		if operation == "none" {
			return nil
		}

		field := g.graphqlFieldFromMethod(method)

		switch operation {
		case "mutation":
			mutations = append(mutations, field)
		case "subscription":
			subscriptions = append(subscriptions, field)
		default:
			queries = append(queries, field)
		}
	}

	if len(queries) > 0 {
		graphqlTypes = append(graphqlTypes, &graphql.Object{
			Name:   g.typeNameMap[service.FullName] + "_Query",
			Fields: queries,
		})
	}
	if len(mutations) > 0 {
		graphqlTypes = append(graphqlTypes, &graphql.Object{
			Name:   g.typeNameMap[service.FullName] + "_Mutation",
			Fields: mutations,
		})
	}
	if len(subscriptions) > 0 {
		graphqlTypes = append(graphqlTypes, &graphql.Object{
			Name:   g.typeNameMap[service.FullName] + "_Subscription",
			Fields: subscriptions,
		})
	}

	return graphqlTypes
}

func (g *Generator) graphqlFieldFromMethod(method *pb.MethodDescriptorProto) *graphql.Field {
	// Only add an argument if there are fields in the gRPC request message.
	var arguments []*graphql.Argument
	inputType := g.messages[method.GetInputType()]
	if len(inputType.Proto.GetField()) != 0 {
		arguments = append(arguments, &graphql.Argument{
			Name:      "input",
			TypeName:  g.inputNameMap[method.GetInputType()],
			Modifiers: graphql.TypeModifierNonNull,
		})
	}

	// If the response message has no fields then return a nullable Boolean.
	// It is up to the resolver's implementation whether or not to return an
	// actual boolean value or default to null.
	outputType := g.messages[method.GetOutputType()]
	if len(outputType.Proto.GetField()) == 0 {
		return &graphql.Field{
			Name:      method.GetName(),
			TypeName:  graphql.ScalarBoolean.TypeName(),
			Arguments: arguments,
		}
	}

	return &graphql.Field{
		Name:      method.GetName(),
		TypeName:  g.typeNameMap[method.GetOutputType()],
		Arguments: arguments,
		Modifiers: graphql.TypeModifierNonNull,
	}
}

type graphqlTypeNameParts struct {
	Package    string
	TypeName   []string
	IsProtoMap bool
	Input      bool
}

func buildGraphqlTypeName(parts *graphqlTypeNameParts) string {
	var b strings.Builder
	b.WriteString(generator.CamelCaseSlice(strings.Split(parts.Package, ".")))
	for i, name := range parts.TypeName {
		if parts.IsProtoMap && i == len(parts.TypeName)-1 {
			name = strings.TrimSuffix(name, "Entry")
		}

		b.WriteString("_")
		b.WriteString(generator.CamelCase(name))
	}
	if parts.Input {
		b.WriteString("_Input")
	}
	return b.String()
}

func graphqlFileName(name string) string {
	return strings.TrimSuffix(name, ".proto") + "_pb.graphql"
}

func stringPtr(v string) *string {
	return &v
}
