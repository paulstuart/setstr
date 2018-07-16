// Package setstr generates string setter functions for Go structs
package setstr

// To regenerage example protobuf code, run "go generate" on the command line
//go:generate protoc -I=pb/ -I=pb/common --go_out=Mbase.proto=github.com/paulstuart/setstr/pb:pb/ pb/example.proto

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

const suffix = "_setters"

var (
	re = regexp.MustCompile(`protobuf:".*name=([_a-zA-Z][a-zA-Z0-9]*),?.*"`)
)

type Meta struct {
	fieldName, fieldType, tagName string
}

type Filter func(fileName, structName, fieldName, fieldType string) bool

func BaseFilter(fileName, structName, fieldName, fieldType string) bool {
	panic("yikes!")
	return true
	return strings.HasSuffix(fieldType, ".Base") || strings.HasSuffix(fieldType, ".Error")
}

// Saver saves the results of the file parsing
type Saver func(fileName, pkgName string, imports Imports, m map[string][]Meta) error

type Import struct {
	Name, Path string
}

type Imports []Import

// Len is for Sort interface
func (imp Imports) Len() int {
	return len(imp)
}

// Less is for Sort interface
func (imp Imports) Less(i, j int) bool {
	return imp[i].Path < imp[j].Path
}

// Swap is for Sort interface
func (imp Imports) Swap(i, j int) {
	imp[i], imp[j] = imp[j], imp[i]
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <dir>\n", os.Args[0])
		os.Exit(1)
	}
	//if err := ParseDir(os.Args[1], nil, fileSaver); err != nil {
	if err := ParseFile(os.Args[1], nil, fileSaver); err != nil {
		panic(err)
	}
}

// nullFilter satisfies filter requirement
func nullFilter(fileName, identity, name, ptr string) bool {
	return true
}

// ParseDir processes functions with comment prefixes
func ParseDir(filePath string, filter Filter, saver Saver) error {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filePath, nil, 0)
	if err != nil {
		return err
	}

	config := &types.Config{
		Error: func(e error) {
			fmt.Println("AIIEE:", e)
		},
		Importer: importer.Default(),
	}

	info := types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	var files []*ast.File
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			files = append(files, file)
		}
	}
	pkg, e := config.Check(filePath, fset, files, &info)
	if e != nil {
		fmt.Println(e)
	} else {
		fmt.Println("PKG:", pkg)
	}

	if filter == nil {
		filter = nullFilter
	}
	for pkgName, pkg := range pkgs {
		for fileName, node := range pkg.Files {
			imports := make(map[string]string)
			mlist := make(map[string][]Meta)
			var identity string
			ast.Inspect(node, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.File:
				case *ast.ImportSpec:
					if n.Name != nil {
						imports[n.Name.Name] = n.Path.Value
					} else {
						key := path.Base(strings.Trim(n.Path.Value, `"`))
						imports[key] = n.Path.Value
					}
				case *ast.Ident:
					identity = n.Name
				case *ast.StructType:
					for _, f := range n.Fields.List {
						tag := tagName(f.Tag.Value)
						if tag == "" {
							continue
						}
						for _, name := range f.Names {
							switch n := f.Type.(type) {
							case *ast.SelectorExpr:
								ptrType := fmt.Sprintf("%s.%s", n.X, n.Sel)
								if filter(fileName, identity, name.Name, ptrType) {
									mlist[identity] = append(mlist[identity], Meta{name.Name, ptrType, tag})
								}
							case *ast.StarExpr:
								if s, ok := n.X.(*ast.SelectorExpr); ok {
									ptrType := fmt.Sprintf("*%s.%s", s.X, s.Sel)
									if filter(fileName, identity, name.Name, ptrType) {
										mlist[identity] = append(mlist[identity], Meta{name.Name, ptrType, tag})
										pp := types.NewPackage(pkgName, pkg.Name)
										tv, err := types.Eval(fset, pp, n.Pos(), fmt.Sprint(n.X))
										if err != nil {
											fmt.Println("TV ERR:", err)
										} else {
											fmt.Println("TV:", tv)
										}
									}
								}
							case *ast.Ident:
								mlist[identity] = append(mlist[identity], Meta{name.Name, fmt.Sprint(f.Type), tag})
							case *ast.ArrayType:
							default:
								fmt.Printf("-->DEFAULT S: %s (%T) N: %s field:%+v (%T::%s)\n", identity, n, name.Name, f, f.Type, f.Type)
							}
							break
						}
					}
					return false
				}
				return true
			})

			if len(mlist) == 0 {
				continue
			}

			// skip our needed imports
			delete(imports, "encoding/json")
			delete(imports, "errors")
			delete(imports, "strconv")

			fmt.Println("IMPORTS:", imports)
			// filter imports for only those used
			var imported Imports
			for _, m := range mlist {
				for _, s := range m {
					spath := strings.Split(s.fieldType, ".")
					// is struct an imported type?
					if len(spath) == 2 {
						pkg := strings.TrimLeft(spath[0], "*")
						if pth, ok := imports[pkg]; ok {
							delete(imports, pkg)
							// delete redundant names
							if pkg == path.Base(strings.Trim(pth, `"`)) {
								pkg = ""
							}
							imported = append(imported, Import{pkg, pth})
						}
					}
				}
			}
			// we need these imports
			imported = append(imported,
				Import{Path: `"encoding/json"`},
				Import{Path: `"errors"`},
				Import{Path: `"strconv"`},
			)
			sort.Sort(imported)
			if err := saver(fileName, pkgName, imported, mlist); err != nil {
				return err
			}
		}
	}
	return nil
}

func ParseFile(fileName string, filter Filter, saver Saver) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, fileName, nil, 0)
	if err != nil {
		return err
	}

	config := &types.Config{
		Error: func(e error) {
			fmt.Println("AIIEE:", e)
		},
		Importer: importer.Default(),
	}

	info := types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	files := []*ast.File{f}
	pkg, e := config.Check(fileName, fset, files, &info)
	if e != nil {
		fmt.Println(e)
	} else {
		fmt.Println("PKG:", pkg)
	}

	if filter == nil {
		filter = nullFilter
	}
	pkgName := "blah"
	imports := make(map[string]string)
	mlist := make(map[string][]Meta)
	var identity string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ImportSpec:
			if n.Name != nil {
				imports[n.Name.Name] = n.Path.Value
			} else {
				key := path.Base(strings.Trim(n.Path.Value, `"`))
				imports[key] = n.Path.Value
			}
		case *ast.Ident:
			identity = n.Name
		case *ast.StructType:
			for _, f := range n.Fields.List {
				tag := tagName(f.Tag.Value)
				if tag == "" {
					continue
				}
				for _, name := range f.Names {
					switch n := f.Type.(type) {
					case *ast.SelectorExpr:
						ptrType := fmt.Sprintf("%s.%s", n.X, n.Sel)
						if filter(fileName, identity, name.Name, ptrType) {
							mlist[identity] = append(mlist[identity], Meta{name.Name, ptrType, tag})
						}
					case *ast.StarExpr:
						if s, ok := n.X.(*ast.SelectorExpr); ok {
							ptrType := fmt.Sprintf("*%s.%s", s.X, s.Sel)
							if filter(fileName, identity, name.Name, ptrType) {
								mlist[identity] = append(mlist[identity], Meta{name.Name, ptrType, tag})
							}
						}
					case *ast.Ident:
						mlist[identity] = append(mlist[identity], Meta{name.Name, fmt.Sprint(f.Type), tag})
					case *ast.ArrayType:
					default:
						fmt.Printf("-->DEFAULT S: %s (%T) N: %s field:%+v (%T::%s)\n", identity, n, name.Name, f, f.Type, f.Type)
					}
					break
				}
			}
			return false
		}
		return true
	})

	if len(mlist) == 0 {
		return nil
	}

	// skip our needed imports
	delete(imports, "encoding/json")
	delete(imports, "errors")
	delete(imports, "strconv")

	// filter imports for only those used
	var imported Imports
	for _, m := range mlist {
		for _, s := range m {
			spath := strings.Split(s.fieldType, ".")
			// is struct an imported type?
			if len(spath) == 2 {
				pkg := strings.TrimLeft(spath[0], "*")
				if pth, ok := imports[pkg]; ok {
					delete(imports, pkg)
					pkgName = pkg

					// delete redundant names
					if pkg == path.Base(strings.Trim(pth, `"`)) {
						pkg = ""
					}
					imported = append(imported, Import{pkg, pth})
				}
			}
		}
	}
	// we need these imports
	imported = append(imported,
		Import{Path: `"encoding/json"`},
		Import{Path: `"errors"`},
		Import{Path: `"strconv"`},
	)
	sort.Sort(imported)
	return saver(fileName, pkgName, imported, mlist)
}

func tagName(t string) string {
	if n := re.FindStringSubmatch(t); len(n) > 1 {
		return n[1]
	}
	return ""
}

// fileSave saves the results of the parse file
func fileSaver(fileName, pkgName string, imports Imports, meta map[string][]Meta) error {
	newName := strings.TrimRight(fileName, ".go") + suffix + ".go"
	w, err := os.Create(newName)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "//")
	fmt.Fprintln(w, "// GENERATED FILE -- DO NOT EDIT")
	fmt.Fprintln(w, "//")
	fmt.Fprintln(w, "// command:", strings.Join(os.Args, " "))
	fmt.Fprintln(w, "//")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "package", pkgName)
	fmt.Fprintln(w)
	if len(imports) > 0 {
		fmt.Fprintln(w, "import (")
		for _, imp := range imports {
			fmt.Fprintf(w, "\t")
			if imp.Name != "" {
				fmt.Fprintf(w, "%s ", imp.Name)
			}
			fmt.Fprintf(w, "%s\n", imp.Path)
		}
		fmt.Fprintln(w, ")")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "// guarantee imports")
	fmt.Fprintln(w, "var _ = strconv.Atoi")
	fmt.Fprintln(w, "var _ = errors.New")
	fmt.Fprintln(w, "var _ = json.Marshal")
	fmt.Fprintln(w)

	ptrs := make(map[string]struct{})
	for structName, list := range meta {
		for _, m := range list {
			SetField(w, structName, m.fieldName, m.fieldType)
			if _, exists := ptrs[structName]; !exists {
				PtrFunc(w, structName)
				ptrs[structName] = struct{}{}
			}
		}
		setStr(w, structName, list)
	}

	return w.Close()
}

func PtrFunc(w io.Writer, name string) {
	const body = `
// Ptr returns a pointer to a copy of %s
func (s %s) Ptr() interface{} {
	return &s
}
`
	fmt.Fprintf(w, body, name, name)
}

func SetField(w io.Writer, structName, field, kind string) {
	const body = `
// Set%s sets %s
func (s *%s) Set%s(v %s) {
	s.%s = v
}
`
	fmt.Fprintf(w, body,
		field, field,
		structName, field, kind,
		field,
	)
}

func setStr(w io.Writer, name string, meta []Meta) {
	const head = `
// SetString sets the named element using the given string
func (s *%s) SetString(name, value string) error {
	var err error
	switch name {
`

	const tail = `	default:
		err = errors.New("field does not exist:" + name)
	}
	return err
}
`
	line := func(text string, args ...interface{}) {
		fmt.Fprint(w, "\t\t")
		fmt.Fprintf(w, text, args...)
	}
	fmt.Fprintf(w, head, name)
	for _, m := range meta {
		var ptr, ref string
		if strings.HasPrefix(m.fieldType, "*") {
			ptr = "*"
		} else {
			ref = "&"
		}
		fmt.Fprintf(w, "\tcase \"%s\":  // (%s)\n", m.tagName, m.fieldType)
		switch m.fieldType {
		case "int":
			line("%ss.%s, err = strconv.Atoi(value)\n", ptr, m.fieldName)
		case "int32":
			line("var v int32")
			line("tv, err = strconv.Atoi(value)\n")
			line("%ss.%s = int32(v)\n", ptr, m.fieldName)
		case "int64":
			line("%ss.%s, err = strconv.ParseInt(value, 10, 64)\n", ptr, m.fieldName)
		case "uint":
			line("var u uint")
			line("u, err = strconv.ParseUint(value, 10, 64)\n")
			line("%ss.%s = uint(u)\n", ptr, m.fieldName)
		case "uint32":
			line("var u uint32")
			line("u, err = strconv.ParseUint(value, 10, 32)\n")
			line("%ss.%s = uint32(u)\n", ptr, m.fieldName)
		case "uint64":
			line("%ss.%s, err = strconv.ParseUint(value, 10, 64)\n", ptr, m.fieldName)
		case "float32":
			line("var v float32")
			line("v, err = strconv.ParseFloat(value, 32)\n")
			line("%ss.%s = float32(v)\n", ptr, m.fieldName)
		case "float64":
			line("%ss.%s, err = strconv.ParseFloat(value, 64)\n", ptr, m.fieldName)
		case "string":
			line("%ss.%s = value\n", ptr, m.fieldName)
		default:
			line("err = json.Unmarshal([]byte(value), %ss.%s)\n", ref, m.fieldName)
		}
	}
	fmt.Fprintf(w, tail)
}
