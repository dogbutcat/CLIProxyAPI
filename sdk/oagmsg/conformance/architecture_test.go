package conformance_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var legacyTranslatorPackageFunctions = map[string]struct{}{
	"SetPluginHooks": {},
}

var legacyTranslatorRegistryMethods = map[string]struct{}{
	"SetPluginHooks": {},
}

func TestProductionConversionDoesNotDependOnTranslator(t *testing.T) {
	repositoryRoot, errAbs := filepath.Abs(filepath.Join("..", "..", ".."))
	if errAbs != nil {
		t.Fatal(errAbs)
	}

	violations, errCheck := checkTranslatorArchitectureInRepository(repositoryRoot)
	if errCheck != nil {
		t.Fatal(errCheck)
	}
	if len(violations) > 0 {
		t.Fatalf("production translator dependency violations:\n%s", strings.Join(formatArchitectureViolations(violations), "\n"))
	}
}

func TestXAIProtocolAdaptationIsOwnedByOAGMsg(t *testing.T) {
	repositoryRoot, errAbs := filepath.Abs(filepath.Join("..", "..", ".."))
	if errAbs != nil {
		t.Fatal(errAbs)
	}

	forbidden := []string{
		"normalizeXAITools",
		"promoteXAIAdditionalTools",
		"normalizeXAINamespaceToolChoice",
		"normalizeXAIInputCustomToolCalls",
		"normalizeXAIInputNamespaceToolCalls",
		"restoreXAINamespaceToolCalls",
		"preserveXAIResponsesOutputControls",
		"xaiInternalXSearchResponseFilter",
	}
	xaiExecutorFiles, errGlob := filepath.Glob(filepath.Join(repositoryRoot, "internal", "runtime", "executor", "xai*.go"))
	if errGlob != nil {
		t.Fatal(errGlob)
	}
	for _, path := range xaiExecutorFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		for _, symbol := range forbidden {
			if strings.Contains(string(source), "func "+symbol) || strings.Contains(string(source), "type "+symbol) {
				t.Errorf("%s declares xAI protocol adapter symbol %s outside sdk/oagmsg", filepath.Base(path), symbol)
			}
		}
	}

	requestPath := filepath.Join(repositoryRoot, "internal", "runtime", "executor", "xai_executor_request.go")
	requestSource, errRead := os.ReadFile(requestPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, call := range []string{
		"oagmsg.PreserveXAIResponsesOutputControls",
		"oagmsg.PrepareXAIResponsesTools",
		"oagmsg.FinalizeXAIResponsesHistory",
	} {
		if !strings.Contains(string(requestSource), call) {
			t.Errorf("xAI request path does not delegate protocol adaptation through %s", call)
		}
	}
}

func TestTranslatorArchitectureCheckerInjectedSources(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "allows type-only format pipeline and plugin hook adapter compatibility",
			src: `package sample

import (
	"context"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type Holder struct {
	Format sdktranslator.Format
	Hooks sdktranslator.PluginHooks
	Pipeline *sdktranslator.Pipeline
}

func use(ctx context.Context, hooks sdktranslator.PluginHooks, h *Holder) {
	_ = sdktranslator.FormatOpenAI
	_ = sdktranslator.FromString("codex")
	_, _ = hooks.TranslateRequest(ctx, "from", "to", "model", nil, false)
	_, _ = h.Hooks.TranslateResponse(ctx, "from", "to", "model", nil, nil, nil, false)
}
`,
		},
		{
			name: "blocks default package conversion helpers",
			src: `package sample

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use() {
	_ = translator.TranslateRequest("from", "to", "model", nil, false)
	_ = translator.HasNonStreamResponseTransformer("from", "to")
	translator.SetPluginHooks(nil)
}
`,
			want: []string{
				"case.go:6 calls sdk/translator.TranslateRequest",
				"case.go:7 calls sdk/translator.HasNonStreamResponseTransformer",
				"case.go:8 calls sdk/translator.SetPluginHooks",
			},
		},
		{
			name: "blocks aliased conversion helpers",
			src: `package sample

import tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use() {
	_ = tr.TranslateStream(nil, "from", "to", "model", nil, nil, nil, nil)
	_ = tr.TranslateTokenCount(nil, "from", "to", 1, nil)
}
`,
			want: []string{
				"case.go:6 calls sdk/translator.TranslateStream",
				"case.go:7 calls sdk/translator.TranslateTokenCount",
			},
		},
		{
			name: "blocks dot and blank imports of plain translator",
			src: `package sample

import . "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
import _ "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
`,
			want: []string{
				"case.go:3 imports sdk/translator as dot import",
				"case.go:4 imports sdk/translator as blank import",
			},
		},
		{
			name: "blocks forbidden translator implementation imports",
			src: `package sample

import _ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai"
import "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtin"

var _ = builtin.DefaultRegistry
`,
			want: []string{
				"case.go:3 imports internal/translator",
				"case.go:4 imports sdk/translator/builtin",
			},
		},
		{
			name: "blocks chained registry and pipeline constructors",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use() {
	_ = sdktranslator.Default().TranslateRequest("from", "to", "model", nil, false)
	_ = sdktranslator.NewRegistry().HasRequestTransformer("from", "to")
	_, _ = sdktranslator.NewPipeline(nil).TranslateResponse(nil, "from", "to", sdktranslator.ResponseEnvelope{}, nil, nil, nil)
}
`,
			want: []string{
				"case.go:6 calls sdk/translator.Registry.TranslateRequest",
				"case.go:7 calls sdk/translator.Registry.HasRequestTransformer",
				"case.go:8 calls sdk/translator.Pipeline.TranslateResponse",
			},
		},
		{
			name: "blocks typed locals parameters and constructor assignments",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use(r *sdktranslator.Registry, p *sdktranslator.Pipeline) {
	r.SetPluginHooks(nil)
	_ = r.TranslateTokenCount(nil, "from", "to", 1, nil)
	_, _ = p.TranslateRequest(nil, "from", "to", sdktranslator.RequestEnvelope{})
	local := sdktranslator.NewRegistry()
	_ = local.TranslateStream(nil, "from", "to", "model", nil, nil, nil, nil)
}
`,
			want: []string{
				"case.go:6 calls sdk/translator.Registry.SetPluginHooks",
				"case.go:7 calls sdk/translator.Registry.TranslateTokenCount",
				"case.go:8 calls sdk/translator.Pipeline.TranslateRequest",
				"case.go:10 calls sdk/translator.Registry.TranslateStream",
			},
		},
		{
			name: "blocks nested typed fields but allows nested plugin hook calls",
			src: `package sample

import (
	"context"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type Holder struct {
	Registry *sdktranslator.Registry
	Inner *Inner
	Hooks sdktranslator.PluginHooks
}

type Inner struct {
	Pipeline *sdktranslator.Pipeline
}

func use(ctx context.Context, h *Holder) {
	_ = h.Registry.TranslateNonStream(ctx, "from", "to", "model", nil, nil, nil, nil)
	_, _ = h.Inner.Pipeline.TranslateResponse(ctx, "from", "to", sdktranslator.ResponseEnvelope{}, nil, nil, nil)
	_, _ = h.Hooks.TranslateRequest(ctx, "from", "to", "model", nil, false)
}
`,
			want: []string{
				"case.go:19 calls sdk/translator.Registry.TranslateNonStream",
				"case.go:20 calls sdk/translator.Pipeline.TranslateResponse",
			},
		},
		{
			name: "blocks calls inside case comm clauses and closures",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use(ch chan int, r *sdktranslator.Registry) {
	switch <-ch {
	case 1:
		_ = sdktranslator.TranslateRequest("from", "to", "model", nil, false)
	default:
		_ = func() []byte {
			return r.TranslateNonStream(nil, "from", "to", "model", nil, nil, nil, nil)
		}()
	}
	select {
	case ch <- 1:
		_ = r.HasStreamResponseTransformer("from", "to")
	default:
	}
label:
	_ = r.TranslateTokenCount(nil, "from", "to", 1, nil)
	_ = label
}
`,
			want: []string{
				"case.go:8 calls sdk/translator.TranslateRequest",
				"case.go:11 calls sdk/translator.Registry.TranslateNonStream",
				"case.go:16 calls sdk/translator.Registry.HasStreamResponseTransformer",
				"case.go:20 calls sdk/translator.Registry.TranslateTokenCount",
			},
		},
		{
			name: "blocks cross file package globals",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

var shared = sdktranslator.NewRegistry()
`,
			want: []string{
				"b.go:4 calls sdk/translator.Registry.TranslateRequest",
			},
		},
		{
			name: "blocks package and method value references",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use(r *sdktranslator.Registry, p *sdktranslator.Pipeline) {
	f := sdktranslator.TranslateRequest
	_ = f
	g := r.TranslateStream
	_ = g
	h := p.TranslateResponse
	_ = h
	ok := r.HasResponseTransformer
	_ = ok
}
`,
			want: []string{
				"case.go:6 references sdk/translator.TranslateRequest",
				"case.go:8 references sdk/translator.Registry.TranslateStream",
				"case.go:10 references sdk/translator.Pipeline.TranslateResponse",
				"case.go:12 references sdk/translator.Registry.HasResponseTransformer",
			},
		},
		{
			name: "blocks ByFormatName package helpers direct and function values",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use() {
	_ = sdktranslator.TranslateRequestByFormatName("from", "to", "model", nil, false)
	_ = sdktranslator.HasRequestTransformerByFormatName("from", "to")
	_ = sdktranslator.TranslateNonStreamByFormatName(nil, "from", "to", "model", nil, nil, nil, nil)
	stream := sdktranslator.TranslateStreamByFormatName
	_ = stream
	tokens := sdktranslator.TranslateTokenCountByFormatName
	_ = tokens
	has := sdktranslator.HasNonStreamResponseTransformerByFormatName
	_ = has
}
`,
			want: []string{
				"case.go:6 calls sdk/translator.TranslateRequestByFormatName",
				"case.go:7 calls sdk/translator.HasRequestTransformerByFormatName",
				"case.go:8 calls sdk/translator.TranslateNonStreamByFormatName",
				"case.go:9 references sdk/translator.TranslateStreamByFormatName",
				"case.go:11 references sdk/translator.TranslateTokenCountByFormatName",
				"case.go:13 references sdk/translator.HasNonStreamResponseTransformerByFormatName",
			},
		},
		{
			name: "allows internal translator imports in test oracle files",
			src: `package sample

import _ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai"

func use() {}
`,
		},
		{
			name: "allows translatorish and builtinextra import path segments",
			src: `package sample

import _ "github.com/router-for-me/CLIProxyAPI/v7/internal/translatorish"
import _ "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtinextra"

func use() {}
`,
		},
		{
			name: "blocks actual import subtrees with exact segment boundaries",
			src: `package sample

import _ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
import _ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai"
import _ "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtin"
import _ "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtin/openai"
`,
			want: []string{
				"case.go:3 imports internal/translator",
				"case.go:4 imports internal/translator",
				"case.go:5 imports sdk/translator/builtin",
				"case.go:6 imports sdk/translator/builtin",
			},
		},
		{
			name: "blocks registry pipeline aliases and embedded promoted calls",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

type RegistryAlias = sdktranslator.Registry
type PipelineAlias = sdktranslator.Pipeline
type RegistryHolder struct {
	*RegistryAlias
}
type PipelineHolder struct {
	*PipelineAlias
}

func use(rh *RegistryHolder, ph *PipelineHolder, r *RegistryAlias, p *PipelineAlias) {
	_ = r.TranslateRequest("from", "to", "model", nil, false)
	_, _ = p.TranslateResponse(nil, "from", "to", sdktranslator.ResponseEnvelope{}, nil, nil, nil)
	_ = rh.TranslateTokenCount(nil, "from", "to", 1, nil)
	_, _ = ph.TranslateResponse(nil, "from", "to", sdktranslator.ResponseEnvelope{}, nil, nil, nil)
}
`,
			want: []string{
				"case.go:15 calls sdk/translator.Registry.TranslateRequest",
				"case.go:16 calls sdk/translator.Pipeline.TranslateResponse",
				"case.go:17 calls sdk/translator.Registry.TranslateTokenCount",
				"case.go:18 calls sdk/translator.Pipeline.TranslateResponse",
			},
		},
		{
			name: "does not treat defined translator-shaped types as aliases",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

type RegistryCopy sdktranslator.Registry
type PipelineCopy sdktranslator.Pipeline

var _ *RegistryCopy
var _ *PipelineCopy
`,
		},
		{
			name: "preserves declared static type across unknown assignment",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func obtain() *sdktranslator.Registry { return nil }

func use() {
	var r *sdktranslator.Registry
	r = obtain()
	_ = r.TranslateRequest("from", "to", "model", nil, false)
}
`,
			want: []string{
				"case.go:10 calls sdk/translator.Registry.TranslateRequest",
			},
		},
		{
			name: "preserves parameter scope through short declarations",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func obtain() (*sdktranslator.Registry, error) { return nil, nil }

func use(r *sdktranslator.Registry) {
	r, err := obtain()
	_ = err
	_ = r.TranslateRequest("from", "to", "model", nil, false)
	func(p *sdktranslator.Registry) {
		p, err := obtain()
		_ = err
		_ = p.TranslateStream(nil, "from", "to", "model", nil, nil, nil, nil)
	}(nil)
}
`,
			want: []string{
				"case.go:10 calls sdk/translator.Registry.TranslateRequest",
				"case.go:14 calls sdk/translator.Registry.TranslateStream",
			},
		},
		{
			name: "allows short declaration shadowing registry with plugin hooks",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

type pluginHooks struct{}

func (pluginHooks) TranslateRequest() {}

func use(r *sdktranslator.Registry, hooks pluginHooks) {
	{
		r := hooks
		r.TranslateRequest()
	}
}
`,
		},
		{
			name: "preserves predeclared range assignment static type",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use(registries []*sdktranslator.Registry) {
	var r *sdktranslator.Registry
	for _, r = range registries {
		_ = r.TranslateRequest("from", "to", "model", nil, false)
	}
}
`,
			want: []string{
				"case.go:8 calls sdk/translator.Registry.TranslateRequest",
			},
		},
		{
			name: "allows local declaration to shadow translator import alias",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

type SomePlugin struct{}

func (SomePlugin) TranslateRequest() {}

func use(sdktranslator SomePlugin) {
	sdktranslator.TranslateRequest()
}
`,
		},
		{
			name: "allows shadowed translator alias constructor methods",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

type pluginRegistry struct{}
type pluginTranslator struct{}

func (pluginTranslator) NewRegistry() pluginRegistry { return pluginRegistry{} }
func (pluginRegistry) TranslateRequest() {}

func use(sdktranslator pluginTranslator) {
	local := sdktranslator.NewRegistry()
	local.TranslateRequest()
}
`,
		},
		{
			name: "blocks forbidden selector references on assignment lhs children",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

func use(m map[any]int) {
	m[sdktranslator.TranslateRequest] = 1
}
`,
			want: []string{
				"case.go:6 references sdk/translator.TranslateRequest",
			},
		},
		{
			name: "blocks recursively promoted embedded registry methods",
			src: `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

type Base struct {
	*sdktranslator.Registry
}
type Mid struct {
	*Base
}
type Holder struct {
	*Mid
}

func use(h *Holder) {
	_ = h.TranslateRequest("from", "to", "model", nil, false)
}
`,
			want: []string{
				"case.go:16 calls sdk/translator.Registry.TranslateRequest",
			},
		},
		{
			name: "resolves reverse filename chained aliases and globals",
			src:  "",
			want: []string{
				"c.go:4 calls sdk/translator.Registry.TranslateRequest",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := map[string]string{"case.go": test.src}
			if test.name == "blocks cross file package globals" {
				sources = map[string]string{
					"a.go": test.src,
					"b.go": `package sample

func use() {
	_ = shared.TranslateRequest("from", "to", "model", nil, false)
}
`,
				}
			}
			if test.name == "allows internal translator imports in test oracle files" {
				sources = map[string]string{"oracle_test.go": test.src}
			}
			if test.name == "resolves reverse filename chained aliases and globals" {
				sources = map[string]string{
					"a.go": `package sample

type AliasA = AliasB
var shared *AliasA
`,
					"b.go": `package sample

import sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"

type AliasB = sdktranslator.Registry
`,
					"c.go": `package sample

func use() {
	_ = shared.TranslateRequest("from", "to", "model", nil, false)
}
`,
				}
			}
			violations, errCheck := checkTranslatorArchitectureSources(sources)
			if errCheck != nil {
				t.Fatal(errCheck)
			}
			got := formatArchitectureViolations(violations)
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("violations mismatch\nwant:\n%s\ngot:\n%s", strings.Join(test.want, "\n"), strings.Join(got, "\n"))
			}
		})
	}
}

func checkTranslatorArchitectureInRepository(repositoryRoot string) ([]architectureViolation, error) {
	var sources = make(map[string]string)
	errWalk := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, errWalk error) error {
		if errWalk != nil {
			return errWalk
		}
		relative, errRelative := filepath.Rel(repositoryRoot, path)
		if errRelative != nil {
			return errRelative
		}
		if entry.IsDir() {
			if shouldSkipArchitectureDirectory(relative) {
				return filepath.SkipDir
			}
			return nil
		}
		if !shouldCheckArchitectureSource(relative) {
			return nil
		}
		body, errRead := os.ReadFile(path)
		if errRead != nil {
			return fmt.Errorf("read %s: %w", relative, errRead)
		}
		sources[relative] = string(body)
		return nil
	})
	if errWalk != nil {
		return nil, errWalk
	}
	return checkTranslatorArchitectureSources(sources)
}

func checkTranslatorArchitectureSources(sources map[string]string) ([]architectureViolation, error) {
	checker := newTranslatorArchitectureChecker()
	for name, source := range sources {
		if !shouldCheckArchitectureSource(name) {
			continue
		}
		if errParse := checker.parseSource(name, source); errParse != nil {
			return nil, errParse
		}
	}
	return checker.check(), nil
}

func shouldSkipArchitectureDirectory(relative string) bool {
	if relative == ".git" || relative == "vendor" || relative == "examples/translator" {
		return true
	}
	return relative == "internal/translator" ||
		strings.HasPrefix(relative, "internal/translator"+string(filepath.Separator)) ||
		relative == "sdk/translator" ||
		strings.HasPrefix(relative, "sdk/translator"+string(filepath.Separator))
}

func shouldCheckArchitectureSource(relative string) bool {
	return strings.HasSuffix(relative, ".go") && !strings.HasSuffix(relative, "_test.go")
}

type translatorObjectKind int

const (
	translatorObjectUnknown translatorObjectKind = iota
	translatorObjectRegistry
	translatorObjectPipeline
	translatorObjectPluginHooks
)

type architectureType struct {
	kind translatorObjectKind
	name string
}

type architectureField struct {
	typ      architectureType
	embedded bool
}

type architectureViolation struct {
	file    string
	line    int
	message string
}

type architectureFile struct {
	name              string
	packageDirectory  string
	fileSet           *token.FileSet
	file              *ast.File
	translatorAliases map[string]struct{}
}

type translatorArchitectureChecker struct {
	files                 []*architectureFile
	typeAliases           map[string]architectureType
	structTypes           map[string]map[string]architectureField
	packageGlobalVarTypes map[string]map[string]architectureType
	violations            []architectureViolation
	violationKeys         map[string]struct{}
}

type architectureScope struct {
	parent       *architectureScope
	vars         map[string]architectureType
	declared     map[string]struct{}
	packageLevel bool
}

func newTranslatorArchitectureChecker() *translatorArchitectureChecker {
	return &translatorArchitectureChecker{
		typeAliases:           make(map[string]architectureType),
		structTypes:           make(map[string]map[string]architectureField),
		packageGlobalVarTypes: make(map[string]map[string]architectureType),
		violationKeys:         make(map[string]struct{}),
	}
}

func (c *translatorArchitectureChecker) parseSource(name, source string) error {
	fileSet := token.NewFileSet()
	parsed, errParse := parser.ParseFile(fileSet, name, source, 0)
	if errParse != nil {
		return fmt.Errorf("parse %s: %w", name, errParse)
	}
	file := &architectureFile{
		name:              name,
		packageDirectory:  filepath.Dir(name),
		fileSet:           fileSet,
		file:              parsed,
		translatorAliases: make(map[string]struct{}),
	}
	if file.packageDirectory == "." {
		file.packageDirectory = ""
	}
	c.files = append(c.files, file)
	return nil
}

func (c *translatorArchitectureChecker) check() []architectureViolation {
	sort.Slice(c.files, func(i, j int) bool {
		return c.files[i].name < c.files[j].name
	})
	for _, file := range c.files {
		c.collectImportFacts(file)
	}
	for _, file := range c.files {
		c.collectTypeAliasFacts(file)
	}
	for _, file := range c.files {
		c.collectStructFacts(file)
	}
	c.collectPackageGlobalFacts()
	for _, file := range c.files {
		c.checkFile(file)
	}
	sort.Slice(c.violations, func(i, j int) bool {
		left, right := c.violations[i], c.violations[j]
		if left.file != right.file {
			return left.file < right.file
		}
		if left.line != right.line {
			return left.line < right.line
		}
		return left.message < right.message
	})
	return c.violations
}

func (c *translatorArchitectureChecker) collectImportFacts(file *architectureFile) {
	for _, imported := range file.file.Imports {
		importPath, errUnquote := strconv.Unquote(imported.Path.Value)
		if errUnquote != nil {
			c.addViolation(file, imported.Pos(), "has invalid import path")
			continue
		}
		if importPathHasSegmentSequence(importPath, "internal", "translator") {
			c.addViolation(file, imported.Pos(), "imports internal/translator")
		}
		if importPathHasSegmentSequence(importPath, "sdk", "translator", "builtin") {
			c.addViolation(file, imported.Pos(), "imports sdk/translator/builtin")
		}
		if !importPathEndsWithSegments(importPath, "sdk", "translator") {
			continue
		}
		alias := "translator"
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		switch alias {
		case ".":
			c.addViolation(file, imported.Pos(), "imports sdk/translator as dot import")
		case "_":
			c.addViolation(file, imported.Pos(), "imports sdk/translator as blank import")
		default:
			file.translatorAliases[alias] = struct{}{}
		}
	}
}

func (c *translatorArchitectureChecker) collectTypeAliasFacts(file *architectureFile) {
	for _, declaration := range file.file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, spec := range generic.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Assign == token.NoPos {
				continue
			}
			typ := c.typeFromExpr(file, typeSpec.Type)
			if typ.kind == translatorObjectUnknown && typ.name == "" {
				continue
			}
			c.typeAliases[c.structKey(file.packageDirectory, typeSpec.Name.Name)] = typ
		}
	}
}

func (c *translatorArchitectureChecker) collectStructFacts(file *architectureFile) {
	for _, declaration := range file.file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, spec := range generic.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := make(map[string]architectureField)
			for _, field := range structType.Fields.List {
				fieldType := c.typeFromExpr(file, field.Type)
				if fieldType.kind == translatorObjectUnknown && fieldType.name == "" {
					continue
				}
				if len(field.Names) == 0 {
					if name := embeddedFieldName(field.Type); name != "" {
						fields[name] = architectureField{typ: fieldType, embedded: true}
					}
					continue
				}
				for _, name := range field.Names {
					fields[name.Name] = architectureField{typ: fieldType}
				}
			}
			if len(fields) > 0 {
				c.structTypes[c.structKey(file.packageDirectory, typeSpec.Name.Name)] = fields
			}
		}
	}
}

func (c *translatorArchitectureChecker) collectPackageGlobalFacts() {
	for {
		changed := false
		for _, file := range c.files {
			packageGlobals := c.packageGlobals(file.packageDirectory)
			scope := newArchitectureScope(nil)
			for name, typ := range packageGlobals {
				scope.declare(name, typ)
			}
			before := len(packageGlobals)
			for _, declaration := range file.file.Decls {
				generic, ok := declaration.(*ast.GenDecl)
				if ok && generic.Tok == token.VAR {
					c.collectValueSpecTypes(file, scope, generic.Specs, packageGlobals)
				}
			}
			if len(packageGlobals) != before {
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

func (c *translatorArchitectureChecker) checkFile(file *architectureFile) {
	for _, declaration := range file.file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			c.checkFuncDecl(file, typed)
		}
	}
}

func (c *translatorArchitectureChecker) checkFuncDecl(file *architectureFile, function *ast.FuncDecl) {
	packageScope := newArchitectureScope(nil)
	packageScope.packageLevel = true
	for name, typ := range c.packageGlobals(file.packageDirectory) {
		packageScope.declare(name, typ)
	}
	scope := newArchitectureScope(packageScope)
	c.addFieldListTypes(file, scope, function.Recv)
	c.addFieldListTypes(file, scope, function.Type.Params)
	c.addFieldListTypes(file, scope, function.Type.Results)
	if function.Body != nil {
		c.checkStmtList(file, scope, function.Body.List)
	}
}

func (c *translatorArchitectureChecker) checkBlockStmt(file *architectureFile, parent *architectureScope, block *ast.BlockStmt) {
	scope := newArchitectureScope(parent)
	c.checkStmtList(file, scope, block.List)
}

func (c *translatorArchitectureChecker) checkStmtList(file *architectureFile, scope *architectureScope, statements []ast.Stmt) {
	for _, statement := range statements {
		c.checkStmt(file, scope, statement)
	}
}

func (c *translatorArchitectureChecker) checkStmt(file *architectureFile, scope *architectureScope, statement ast.Stmt) {
	switch typed := statement.(type) {
	case *ast.AssignStmt:
		for _, rhs := range typed.Rhs {
			c.checkExpr(file, scope, rhs)
		}
		for _, lhs := range typed.Lhs {
			c.checkAssignmentLHS(file, scope, lhs)
		}
		c.applyAssignmentTypes(file, scope, typed.Tok, typed.Lhs, typed.Rhs)
	case *ast.BlockStmt:
		c.checkBlockStmt(file, scope, typed)
	case *ast.BranchStmt:
	case *ast.DeclStmt:
		generic, ok := typed.Decl.(*ast.GenDecl)
		if ok && generic.Tok == token.VAR {
			c.collectValueSpecTypes(file, scope, generic.Specs, nil)
		}
	case *ast.CaseClause:
		for _, expression := range typed.List {
			c.checkExpr(file, scope, expression)
		}
		caseScope := newArchitectureScope(scope)
		for _, child := range typed.Body {
			c.checkStmt(file, caseScope, child)
		}
	case *ast.CommClause:
		if typed.Comm != nil {
			c.checkStmt(file, scope, typed.Comm)
		}
		commScope := newArchitectureScope(scope)
		for _, child := range typed.Body {
			c.checkStmt(file, commScope, child)
		}
	case *ast.DeferStmt:
		c.checkExpr(file, scope, typed.Call)
	case *ast.ExprStmt:
		c.checkExpr(file, scope, typed.X)
	case *ast.ForStmt:
		loopScope := newArchitectureScope(scope)
		if typed.Init != nil {
			c.checkStmt(file, loopScope, typed.Init)
		}
		if typed.Cond != nil {
			c.checkExpr(file, loopScope, typed.Cond)
		}
		if typed.Post != nil {
			c.checkStmt(file, loopScope, typed.Post)
		}
		if typed.Body != nil {
			c.checkBlockStmt(file, loopScope, typed.Body)
		}
	case *ast.GoStmt:
		c.checkExpr(file, scope, typed.Call)
	case *ast.IfStmt:
		ifScope := newArchitectureScope(scope)
		if typed.Init != nil {
			c.checkStmt(file, ifScope, typed.Init)
		}
		if typed.Cond != nil {
			c.checkExpr(file, ifScope, typed.Cond)
		}
		if typed.Body != nil {
			c.checkBlockStmt(file, ifScope, typed.Body)
		}
		if typed.Else != nil {
			c.checkStmt(file, ifScope, typed.Else)
		}
	case *ast.IncDecStmt:
		c.checkExpr(file, scope, typed.X)
	case *ast.LabeledStmt:
		c.checkStmt(file, scope, typed.Stmt)
	case *ast.RangeStmt:
		rangeScope := newArchitectureScope(scope)
		c.checkExpr(file, rangeScope, typed.X)
		c.checkAssignmentLHS(file, rangeScope, typed.Key)
		c.checkAssignmentLHS(file, rangeScope, typed.Value)
		c.applyRangeAssignmentTypes(rangeScope, typed)
		if typed.Body != nil {
			c.checkBlockStmt(file, rangeScope, typed.Body)
		}
	case *ast.ReturnStmt:
		for _, result := range typed.Results {
			c.checkExpr(file, scope, result)
		}
	case *ast.SelectStmt:
		if typed.Body != nil {
			for _, child := range typed.Body.List {
				c.checkStmt(file, scope, child)
			}
		}
	case *ast.SendStmt:
		c.checkExpr(file, scope, typed.Chan)
		c.checkExpr(file, scope, typed.Value)
	case *ast.SwitchStmt:
		switchScope := newArchitectureScope(scope)
		if typed.Init != nil {
			c.checkStmt(file, switchScope, typed.Init)
		}
		if typed.Tag != nil {
			c.checkExpr(file, switchScope, typed.Tag)
		}
		if typed.Body != nil {
			for _, child := range typed.Body.List {
				c.checkStmt(file, switchScope, child)
			}
		}
	case *ast.TypeSwitchStmt:
		switchScope := newArchitectureScope(scope)
		if typed.Init != nil {
			c.checkStmt(file, switchScope, typed.Init)
		}
		if typed.Assign != nil {
			c.checkStmt(file, switchScope, typed.Assign)
		}
		if typed.Body != nil {
			for _, child := range typed.Body.List {
				c.checkStmt(file, switchScope, child)
			}
		}
	}
}

func (c *translatorArchitectureChecker) checkExpr(file *architectureFile, scope *architectureScope, expression ast.Expr) {
	switch typed := expression.(type) {
	case *ast.ArrayType:
		c.checkExpr(file, scope, typed.Elt)
	case *ast.BinaryExpr:
		c.checkExpr(file, scope, typed.X)
		c.checkExpr(file, scope, typed.Y)
	case *ast.CallExpr:
		c.checkCallExpr(file, scope, typed)
	case *ast.CompositeLit:
		for _, element := range typed.Elts {
			c.checkExpr(file, scope, element)
		}
	case *ast.FuncLit:
		litScope := newArchitectureScope(scope)
		c.addFieldListTypes(file, litScope, typed.Type.Params)
		c.addFieldListTypes(file, litScope, typed.Type.Results)
		if typed.Body != nil {
			c.checkStmtList(file, litScope, typed.Body.List)
		}
	case *ast.IndexExpr:
		c.checkExpr(file, scope, typed.X)
		c.checkExpr(file, scope, typed.Index)
	case *ast.IndexListExpr:
		c.checkExpr(file, scope, typed.X)
		for _, index := range typed.Indices {
			c.checkExpr(file, scope, index)
		}
	case *ast.KeyValueExpr:
		c.checkExpr(file, scope, typed.Key)
		c.checkExpr(file, scope, typed.Value)
	case *ast.MapType:
		c.checkExpr(file, scope, typed.Key)
		c.checkExpr(file, scope, typed.Value)
	case *ast.ParenExpr:
		c.checkExpr(file, scope, typed.X)
	case *ast.SelectorExpr:
		c.checkSelectorReference(file, scope, typed)
		c.checkExpr(file, scope, typed.X)
	case *ast.SliceExpr:
		c.checkExpr(file, scope, typed.X)
		if typed.Low != nil {
			c.checkExpr(file, scope, typed.Low)
		}
		if typed.High != nil {
			c.checkExpr(file, scope, typed.High)
		}
		if typed.Max != nil {
			c.checkExpr(file, scope, typed.Max)
		}
	case *ast.StarExpr:
		c.checkExpr(file, scope, typed.X)
	case *ast.TypeAssertExpr:
		c.checkExpr(file, scope, typed.X)
	case *ast.UnaryExpr:
		c.checkExpr(file, scope, typed.X)
	}
}

func (c *translatorArchitectureChecker) checkCallExpr(file *architectureFile, scope *architectureScope, call *ast.CallExpr) {
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		if packageIdentifier, okIdent := selector.X.(*ast.Ident); okIdent {
			if c.isTranslatorPackageAlias(file, scope, packageIdentifier.Name) && isLegacyTranslatorPackageFunction(selector.Sel.Name) {
				c.addViolation(file, selector.Pos(), fmt.Sprintf("calls sdk/translator.%s", selector.Sel.Name))
			}
		}
		receiverType := c.methodOwnerType(file, scope, selector)
		switch receiverType.kind {
		case translatorObjectRegistry:
			if isLegacyTranslatorRegistryMethod(selector.Sel.Name) {
				c.addViolation(file, selector.Pos(), fmt.Sprintf("calls sdk/translator.Registry.%s", selector.Sel.Name))
			}
		case translatorObjectPipeline:
			if isLegacyTranslatorPipelineMethod(selector.Sel.Name) {
				c.addViolation(file, selector.Pos(), fmt.Sprintf("calls sdk/translator.Pipeline.%s", selector.Sel.Name))
			}
		}
		c.checkExpr(file, scope, selector.X)
	} else {
		c.checkExpr(file, scope, call.Fun)
	}
	for _, arg := range call.Args {
		c.checkExpr(file, scope, arg)
	}
}

func (c *translatorArchitectureChecker) checkSelectorReference(file *architectureFile, scope *architectureScope, selector *ast.SelectorExpr) {
	if packageIdentifier, okIdent := selector.X.(*ast.Ident); okIdent {
		if c.isTranslatorPackageAlias(file, scope, packageIdentifier.Name) && isLegacyTranslatorPackageFunction(selector.Sel.Name) {
			c.addViolation(file, selector.Pos(), fmt.Sprintf("references sdk/translator.%s", selector.Sel.Name))
			return
		}
	}
	receiverType := c.methodOwnerType(file, scope, selector)
	switch receiverType.kind {
	case translatorObjectRegistry:
		if isLegacyTranslatorRegistryMethod(selector.Sel.Name) {
			c.addViolation(file, selector.Pos(), fmt.Sprintf("references sdk/translator.Registry.%s", selector.Sel.Name))
		}
	case translatorObjectPipeline:
		if isLegacyTranslatorPipelineMethod(selector.Sel.Name) {
			c.addViolation(file, selector.Pos(), fmt.Sprintf("references sdk/translator.Pipeline.%s", selector.Sel.Name))
		}
	}
}

func (c *translatorArchitectureChecker) collectValueSpecTypes(file *architectureFile, scope *architectureScope, specs []ast.Spec, target map[string]architectureType) {
	for _, spec := range specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, value := range valueSpec.Values {
			c.checkExpr(file, scope, value)
		}
		for index, name := range valueSpec.Names {
			if name.Name == "_" {
				continue
			}
			var typ architectureType
			if valueSpec.Type != nil {
				typ = c.typeFromExpr(file, valueSpec.Type)
			}
			if typ.kind == translatorObjectUnknown && typ.name == "" && index < len(valueSpec.Values) {
				typ = c.exprType(file, scope, valueSpec.Values[index])
			}
			if typ.kind == translatorObjectUnknown && typ.name == "" {
				scope.declareUnknown(name.Name)
				continue
			}
			if target != nil {
				target[name.Name] = typ
				scope.declare(name.Name, typ)
			} else {
				scope.declare(name.Name, typ)
			}
		}
	}
}

func (c *translatorArchitectureChecker) addFieldListTypes(file *architectureFile, scope *architectureScope, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		typ := c.typeFromExpr(file, field.Type)
		if typ.kind == translatorObjectUnknown && typ.name == "" {
			for _, name := range field.Names {
				if name.Name != "_" {
					scope.declareUnknown(name.Name)
				}
			}
			continue
		}
		for _, name := range field.Names {
			if name.Name != "_" {
				scope.declare(name.Name, typ)
			}
		}
	}
}

func (c *translatorArchitectureChecker) applyAssignmentTypes(file *architectureFile, scope *architectureScope, tokenType token.Token, left []ast.Expr, right []ast.Expr) {
	for index, lhs := range left {
		identifier, ok := lhs.(*ast.Ident)
		if !ok || identifier.Name == "_" {
			continue
		}
		if tokenType != token.DEFINE {
			continue
		}
		var typ architectureType
		if index < len(right) {
			typ = c.exprType(file, scope, right[index])
		}
		if typ.kind == translatorObjectUnknown && typ.name == "" {
			if !scope.declaredInCurrent(identifier.Name) {
				scope.declareUnknown(identifier.Name)
			}
			continue
		}
		if !scope.declaredInCurrent(identifier.Name) {
			scope.declare(identifier.Name, typ)
			continue
		}
		scope.assignKnown(identifier.Name, typ)
	}
}

func (c *translatorArchitectureChecker) applyRangeAssignmentTypes(scope *architectureScope, statement *ast.RangeStmt) {
	if statement == nil {
		return
	}
	c.applySingleRangeAssignmentType(scope, statement.Tok, statement.Key)
	c.applySingleRangeAssignmentType(scope, statement.Tok, statement.Value)
}

func (c *translatorArchitectureChecker) applySingleRangeAssignmentType(scope *architectureScope, tokenType token.Token, lhs ast.Expr) {
	identifier, ok := lhs.(*ast.Ident)
	if !ok || identifier.Name == "_" {
		return
	}
	if tokenType == token.DEFINE && !scope.declaredInCurrent(identifier.Name) {
		scope.declareUnknown(identifier.Name)
	}
}

func (c *translatorArchitectureChecker) checkAssignmentLHS(file *architectureFile, scope *architectureScope, expression ast.Expr) {
	switch typed := expression.(type) {
	case *ast.Ident:
	case *ast.IndexExpr:
		c.checkExpr(file, scope, typed.X)
		c.checkExpr(file, scope, typed.Index)
	case *ast.IndexListExpr:
		c.checkExpr(file, scope, typed.X)
		for _, index := range typed.Indices {
			c.checkExpr(file, scope, index)
		}
	case *ast.ParenExpr:
		c.checkAssignmentLHS(file, scope, typed.X)
	case *ast.SelectorExpr:
		c.checkExpr(file, scope, typed.X)
	case *ast.SliceExpr:
		c.checkExpr(file, scope, typed.X)
		if typed.Low != nil {
			c.checkExpr(file, scope, typed.Low)
		}
		if typed.High != nil {
			c.checkExpr(file, scope, typed.High)
		}
		if typed.Max != nil {
			c.checkExpr(file, scope, typed.Max)
		}
	case *ast.StarExpr:
		c.checkExpr(file, scope, typed.X)
	case *ast.TypeAssertExpr:
		c.checkExpr(file, scope, typed.X)
	default:
		if expression != nil {
			c.checkExpr(file, scope, expression)
		}
	}
}

func (c *translatorArchitectureChecker) exprType(file *architectureFile, scope *architectureScope, expression ast.Expr) architectureType {
	switch typed := expression.(type) {
	case *ast.CallExpr:
		return c.callReturnType(file, scope, typed)
	case *ast.CompositeLit:
		return c.typeFromExpr(file, typed.Type)
	case *ast.Ident:
		return scope.lookup(typed.Name)
	case *ast.ParenExpr:
		return c.exprType(file, scope, typed.X)
	case *ast.SelectorExpr:
		base := c.exprType(file, scope, typed.X)
		if base.name == "" {
			return architectureType{}
		}
		fields := c.structTypes[c.structKey(file.packageDirectory, base.name)]
		return fields[typed.Sel.Name].typ
	case *ast.UnaryExpr:
		if typed.Op == token.AND {
			return c.exprType(file, scope, typed.X)
		}
	}
	return architectureType{}
}

func (c *translatorArchitectureChecker) callReturnType(file *architectureFile, scope *architectureScope, call *ast.CallExpr) architectureType {
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		if packageIdentifier, okIdent := selector.X.(*ast.Ident); okIdent {
			if c.isTranslatorPackageAlias(file, scope, packageIdentifier.Name) {
				switch selector.Sel.Name {
				case "Default", "NewRegistry":
					return architectureType{kind: translatorObjectRegistry}
				case "NewPipeline":
					return architectureType{kind: translatorObjectPipeline}
				}
			}
		}
	}
	if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "new" && len(call.Args) == 1 {
		return c.typeFromExpr(file, call.Args[0])
	}
	_ = scope
	return architectureType{}
}

func (c *translatorArchitectureChecker) typeFromExpr(file *architectureFile, expression ast.Expr) architectureType {
	switch typed := expression.(type) {
	case *ast.Ident:
		if alias := c.typeAliases[c.structKey(file.packageDirectory, typed.Name)]; alias.kind != translatorObjectUnknown || alias.name != "" {
			return c.resolveArchitectureType(file, alias, nil)
		}
		return architectureType{name: typed.Name}
	case *ast.ParenExpr:
		return c.typeFromExpr(file, typed.X)
	case *ast.SelectorExpr:
		identifier, ok := typed.X.(*ast.Ident)
		if !ok {
			return architectureType{}
		}
		if _, okAlias := file.translatorAliases[identifier.Name]; !okAlias {
			return architectureType{}
		}
		switch typed.Sel.Name {
		case "Registry":
			return architectureType{kind: translatorObjectRegistry}
		case "Pipeline":
			return architectureType{kind: translatorObjectPipeline}
		case "PluginHooks":
			return architectureType{kind: translatorObjectPluginHooks}
		}
	case *ast.StarExpr:
		return c.typeFromExpr(file, typed.X)
	}
	return architectureType{}
}

func (c *translatorArchitectureChecker) resolveArchitectureType(file *architectureFile, typ architectureType, seen map[string]struct{}) architectureType {
	if typ.kind != translatorObjectUnknown || typ.name == "" {
		return typ
	}
	key := c.structKey(file.packageDirectory, typ.name)
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if _, ok := seen[key]; ok {
		return typ
	}
	seen[key] = struct{}{}
	alias, ok := c.typeAliases[key]
	if !ok {
		return typ
	}
	return c.resolveArchitectureType(file, alias, seen)
}

func (c *translatorArchitectureChecker) methodOwnerType(file *architectureFile, scope *architectureScope, selector *ast.SelectorExpr) architectureType {
	receiverType := c.resolveArchitectureType(file, c.exprType(file, scope, selector.X), nil)
	if receiverType.kind == translatorObjectRegistry || receiverType.kind == translatorObjectPipeline {
		return receiverType
	}
	if receiverType.name == "" {
		return architectureType{}
	}
	return c.promotedMethodOwnerType(file, receiverType.name, selector.Sel.Name, nil)
}

func (c *translatorArchitectureChecker) promotedMethodOwnerType(file *architectureFile, typeName, methodName string, seen map[string]struct{}) architectureType {
	key := c.structKey(file.packageDirectory, typeName)
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if _, ok := seen[key]; ok {
		return architectureType{}
	}
	seen[key] = struct{}{}
	for _, field := range c.structTypes[key] {
		if !field.embedded {
			continue
		}
		fieldType := c.resolveArchitectureType(file, field.typ, nil)
		switch fieldType.kind {
		case translatorObjectRegistry:
			if isLegacyTranslatorRegistryMethod(methodName) {
				return fieldType
			}
		case translatorObjectPipeline:
			if isLegacyTranslatorPipelineMethod(methodName) {
				return fieldType
			}
		default:
			if fieldType.name != "" {
				if promoted := c.promotedMethodOwnerType(file, fieldType.name, methodName, seen); promoted.kind != translatorObjectUnknown {
					return promoted
				}
			}
		}
	}
	return architectureType{}
}

func (c *translatorArchitectureChecker) isTranslatorPackageAlias(file *architectureFile, scope *architectureScope, name string) bool {
	if _, ok := file.translatorAliases[name]; !ok {
		return false
	}
	return !scope.declaredBeforePackageScope(name)
}

func (c *translatorArchitectureChecker) addViolation(file *architectureFile, position token.Pos, message string) {
	key := fmt.Sprintf("%s:%d:%s", file.name, file.fileSet.Position(position).Line, message)
	if _, ok := c.violationKeys[key]; ok {
		return
	}
	c.violationKeys[key] = struct{}{}
	c.violations = append(c.violations, architectureViolation{
		file:    file.name,
		line:    file.fileSet.Position(position).Line,
		message: message,
	})
}

func (c *translatorArchitectureChecker) structKey(packageDirectory, name string) string {
	return packageDirectory + "\x00" + name
}

func (c *translatorArchitectureChecker) packageGlobals(packageDirectory string) map[string]architectureType {
	globals := c.packageGlobalVarTypes[packageDirectory]
	if globals == nil {
		globals = make(map[string]architectureType)
		c.packageGlobalVarTypes[packageDirectory] = globals
	}
	return globals
}

func newArchitectureScope(parent *architectureScope) *architectureScope {
	return &architectureScope{
		parent:   parent,
		vars:     make(map[string]architectureType),
		declared: make(map[string]struct{}),
	}
}

func (s *architectureScope) lookup(name string) architectureType {
	for current := s; current != nil; current = current.parent {
		if typ, ok := current.vars[name]; ok {
			return typ
		}
	}
	return architectureType{}
}

func (s *architectureScope) declare(name string, typ architectureType) {
	s.declared[name] = struct{}{}
	s.vars[name] = typ
}

func (s *architectureScope) declareUnknown(name string) {
	s.declared[name] = struct{}{}
	delete(s.vars, name)
}

func (s *architectureScope) declaredInCurrent(name string) bool {
	_, ok := s.declared[name]
	return ok
}

func (s *architectureScope) declaredBeforePackageScope(name string) bool {
	for current := s; current != nil; current = current.parent {
		if current.packageLevel {
			return false
		}
		if _, ok := current.declared[name]; ok {
			return true
		}
	}
	return false
}

func (s *architectureScope) assignKnown(name string, typ architectureType) {
	for current := s; current != nil; current = current.parent {
		if _, ok := current.declared[name]; ok {
			current.vars[name] = typ
			return
		}
	}
	s.declare(name, typ)
}

func isLegacyTranslatorPackageFunction(name string) bool {
	if _, ok := legacyTranslatorPackageFunctions[name]; ok {
		return true
	}
	return isTranslateMethod(name) || isHasMethod(name)
}

func isLegacyTranslatorRegistryMethod(name string) bool {
	if _, ok := legacyTranslatorRegistryMethods[name]; ok {
		return true
	}
	return isTranslateMethod(name) || isHasMethod(name)
}

func isLegacyTranslatorPipelineMethod(name string) bool {
	return isTranslateMethod(name)
}

func isTranslateMethod(name string) bool {
	return strings.HasPrefix(name, "Translate")
}

func isHasMethod(name string) bool {
	return strings.HasPrefix(name, "Has")
}

func embeddedFieldName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.StarExpr:
		return embeddedFieldName(typed.X)
	}
	return ""
}

func importPathHasSegmentSequence(importPath string, sequence ...string) bool {
	segments := strings.Split(importPath, "/")
	if len(sequence) == 0 || len(sequence) > len(segments) {
		return false
	}
	for start := 0; start <= len(segments)-len(sequence); start++ {
		matched := true
		for index, segment := range sequence {
			if segments[start+index] != segment {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func importPathEndsWithSegments(importPath string, suffix ...string) bool {
	segments := strings.Split(importPath, "/")
	if len(suffix) == 0 || len(suffix) > len(segments) {
		return false
	}
	offset := len(segments) - len(suffix)
	for index, segment := range suffix {
		if segments[offset+index] != segment {
			return false
		}
	}
	return true
}

func formatArchitectureViolations(violations []architectureViolation) []string {
	formatted := make([]string, 0, len(violations))
	for _, violation := range violations {
		formatted = append(formatted, fmt.Sprintf("%s:%d %s", violation.file, violation.line, violation.message))
	}
	return formatted
}
