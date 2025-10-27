package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"log"
	"os"
	"strings"
)

func main() {
	// Parse command-line arguments
	inputFile := flag.String("input", "", "Path to the Go file containing AWS Lambda handler")
	outputFile := flag.String("output", "", "Path to write the modified Go file (optional, defaults to stdout)")
	flag.Parse()

	if *inputFile == "" {
		log.Fatal("Please provide an input file using -input flag")
	}

	// Read the input file
	content, err := os.ReadFile(*inputFile)
	if err != nil {
		log.Fatalf("Failed to read input file: %v", err)
	}

	// Parse the Go source code
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, *inputFile, content, parser.ParseComments)
	if err != nil {
		log.Fatalf("Failed to parse Go file: %v", err)
	}

	// Find the lambda.Start call and extract handler function name
	handlerFuncName, err := findLambdaHandler(file)
	if err != nil {
		log.Fatalf("Failed to find lambda handler: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Found Lambda handler: %s\n", handlerFuncName)

	// Analyze the handler function signature
	handlerSig, err := analyzeHandlerSignature(file, handlerFuncName)
	if err != nil {
		log.Fatalf("Failed to analyze handler signature: %v", err)
	}

	// Transform the AST
	transformAST(file, handlerFuncName, handlerSig)

	// Write the output
	var output *os.File
	if *outputFile != "" {
		output, err = os.Create(*outputFile)
		if err != nil {
			log.Fatalf("Failed to create output file: %v", err)
		}
		defer output.Close()
	} else {
		output = os.Stdout
	}

	// Print the modified AST
	if err := printer.Fprint(output, fset, file); err != nil {
		log.Fatalf("Failed to print modified code: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully transformed Lambda handler to Knative function\n")
}

// HandlerSignature describes the Lambda handler function signature
type HandlerSignature struct {
	HasContext bool
	HasInput   bool
	HasOutput  bool
	HasError   bool
}

// analyzeHandlerSignature analyzes the handler function signature
func analyzeHandlerSignature(file *ast.File, handlerName string) (*HandlerSignature, error) {
	var sig *HandlerSignature

	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == handlerName {
			sig = &HandlerSignature{}

			// Analyze parameters
			if fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
				numParams := len(fn.Type.Params.List)
				if numParams >= 1 {
					// Check if first param is context.Context
					firstParam := fn.Type.Params.List[0]
					if selExpr, ok := firstParam.Type.(*ast.SelectorExpr); ok {
						if ident, ok := selExpr.X.(*ast.Ident); ok {
							if ident.Name == "context" && selExpr.Sel.Name == "Context" {
								sig.HasContext = true
								if numParams == 2 {
									sig.HasInput = true
								}
							}
						}
					} else if numParams == 1 {
						// Single param that's not context
						sig.HasInput = true
					}
				}
			}

			// Analyze return values
			if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
				numResults := len(fn.Type.Results.List)
				if numResults == 1 {
					// Check if it's an error
					if ident, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok {
						if ident.Name == "error" {
							sig.HasError = true
						}
					}
				} else if numResults == 2 {
					// (TOut, error)
					sig.HasOutput = true
					sig.HasError = true
				}
			}

			return false
		}
		return true
	})

	if sig == nil {
		return nil, fmt.Errorf("handler function %s not found", handlerName)
	}

	return sig, nil
}

// findLambdaHandler searches for lambda.Start() call and returns the handler function name
func findLambdaHandler(file *ast.File) (string, error) {
	var handlerName string
	var foundMain bool

	ast.Inspect(file, func(n ast.Node) bool {
		// Look for the main function
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
			foundMain = true
			// Look for lambda.Start() call within main
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if callExpr, ok := n.(*ast.CallExpr); ok {
					if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
						// Check if it's a call to lambda.Start
						if ident, ok := selExpr.X.(*ast.Ident); ok {
							if ident.Name == "lambda" && selExpr.Sel.Name == "Start" {
								// Extract the handler function name
								if len(callExpr.Args) > 0 {
									if handlerIdent, ok := callExpr.Args[0].(*ast.Ident); ok {
										handlerName = handlerIdent.Name
										return false
									}
								}
							}
						}
					}
				}
				return true
			})
		}
		return true
	})

	if !foundMain {
		return "", fmt.Errorf("main function not found")
	}

	if handlerName == "" {
		return "", fmt.Errorf("lambda.Start() call not found in main function")
	}

	return handlerName, nil
}

// transformAST modifies the AST to replace main() with Knative handler structure
func transformAST(file *ast.File, handlerFuncName string, handlerSig *HandlerSignature) {
	// Remove lambda import if present
	removeLambdaImport(file)

	// Add context, net/http, and io imports if not present and get their aliases
	contextAlias, httpAlias, ioAlias := addRequiredImports(file, handlerSig)

	// Find and transform the main function
	for i, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
			// Create Handler struct, New function, and Handle method
			handlerStruct := createHandlerStruct()
			newFunc := createNewFunc()
			handleMethod := createHandleMethod(handlerFuncName, contextAlias, httpAlias, ioAlias, handlerSig)

			// Replace main with the new declarations
			newDecls := make([]ast.Decl, 0, len(file.Decls)+2)
			newDecls = append(newDecls, file.Decls[:i]...)
			newDecls = append(newDecls, handlerStruct)
			newDecls = append(newDecls, newFunc)
			newDecls = append(newDecls, handleMethod)
			newDecls = append(newDecls, file.Decls[i+1:]...)
			file.Decls = newDecls
			break
		}
	}
}

// removeLambdaImport removes the AWS Lambda SDK import
func removeLambdaImport(file *ast.File) {
	for i, decl := range file.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok && genDecl.Tok == token.IMPORT {
			// Filter out lambda imports
			var newSpecs []ast.Spec
			for _, spec := range genDecl.Specs {
				if importSpec, ok := spec.(*ast.ImportSpec); ok {
					importPath := strings.Trim(importSpec.Path.Value, `"`)
					// Remove aws-lambda-go imports
					if !strings.Contains(importPath, "aws-lambda-go") {
						newSpecs = append(newSpecs, spec)
					}
				}
			}
			if len(newSpecs) == 0 {
				// Remove the entire import declaration if empty
				file.Decls = append(file.Decls[:i], file.Decls[i+1:]...)
			} else {
				genDecl.Specs = newSpecs
			}
		}
	}
}

// addRequiredImports adds required imports based on handler signature
// Returns the package names/aliases to use for context, http, and io
func addRequiredImports(file *ast.File, handlerSig *HandlerSignature) (contextAlias, httpAlias, ioAlias string) {
	// Check existing imports and get their aliases
	contextAlias = "context"
	httpAlias = "http"
	ioAlias = "io"
	hasContext := false
	hasNetHTTP := false
	hasIO := false
	hasJSON := false
	hasLog := false

	for _, decl := range file.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok && genDecl.Tok == token.IMPORT {
			for _, spec := range genDecl.Specs {
				if importSpec, ok := spec.(*ast.ImportSpec); ok {
					importPath := strings.Trim(importSpec.Path.Value, `"`)
					if importPath == "context" {
						hasContext = true
						if importSpec.Name != nil {
							contextAlias = importSpec.Name.Name
						}
					}
					if importPath == "net/http" {
						hasNetHTTP = true
						if importSpec.Name != nil {
							httpAlias = importSpec.Name.Name
						}
					}
					if importPath == "io" {
						hasIO = true
						if importSpec.Name != nil {
							ioAlias = importSpec.Name.Name
						}
					}
					if importPath == "encoding/json" {
						hasJSON = true
					}
					if importPath == "log" {
						hasLog = true
					}
				}
			}
		}
	}

	// Determine what imports we need
	needIO := handlerSig.HasInput
	needJSON := handlerSig.HasOutput
	needLog := handlerSig.HasError

	// Add missing imports
	if !hasContext || !hasNetHTTP || (needIO && !hasIO) || (needJSON && !hasJSON) || (needLog && !hasLog) {
		for i, decl := range file.Decls {
			if genDecl, ok := decl.(*ast.GenDecl); ok && genDecl.Tok == token.IMPORT {
				if !hasContext {
					genDecl.Specs = append(genDecl.Specs, &ast.ImportSpec{
						Path: &ast.BasicLit{Kind: token.STRING, Value: `"context"`},
					})
				}
				if needJSON && !hasJSON {
					genDecl.Specs = append(genDecl.Specs, &ast.ImportSpec{
						Path: &ast.BasicLit{Kind: token.STRING, Value: `"encoding/json"`},
					})
				}
				if needIO && !hasIO {
					genDecl.Specs = append(genDecl.Specs, &ast.ImportSpec{
						Path: &ast.BasicLit{Kind: token.STRING, Value: `"io"`},
					})
				}
				if needLog && !hasLog {
					genDecl.Specs = append(genDecl.Specs, &ast.ImportSpec{
						Path: &ast.BasicLit{Kind: token.STRING, Value: `"log"`},
					})
				}
				if !hasNetHTTP {
					genDecl.Specs = append(genDecl.Specs, &ast.ImportSpec{
						Path: &ast.BasicLit{Kind: token.STRING, Value: `"net/http"`},
					})
				}
				file.Decls[i] = genDecl
				return contextAlias, httpAlias, ioAlias
			}
		}

		// If no import declaration exists, create one
		var specs []ast.Spec
		specs = append(specs, &ast.ImportSpec{
			Path: &ast.BasicLit{Kind: token.STRING, Value: `"context"`},
		})
		if needJSON {
			specs = append(specs, &ast.ImportSpec{
				Path: &ast.BasicLit{Kind: token.STRING, Value: `"encoding/json"`},
			})
		}
		if needIO {
			specs = append(specs, &ast.ImportSpec{
				Path: &ast.BasicLit{Kind: token.STRING, Value: `"io"`},
			})
		}
		if needLog {
			specs = append(specs, &ast.ImportSpec{
				Path: &ast.BasicLit{Kind: token.STRING, Value: `"log"`},
			})
		}
		specs = append(specs, &ast.ImportSpec{
			Path: &ast.BasicLit{Kind: token.STRING, Value: `"net/http"`},
		})

		newImport := &ast.GenDecl{Tok: token.IMPORT, Specs: specs}
		file.Decls = append([]ast.Decl{newImport}, file.Decls...)
	}

	return contextAlias, httpAlias, ioAlias
}

// createHandlerStruct creates the Handler struct declaration
func createHandlerStruct() *ast.GenDecl {
	return &ast.GenDecl{
		Tok: token.TYPE,
		Specs: []ast.Spec{
			&ast.TypeSpec{
				Name: ast.NewIdent("Handler"),
				Type: &ast.StructType{
					Fields: &ast.FieldList{},
				},
			},
		},
	}
}

// createNewFunc creates the New() function that returns *Handler
func createNewFunc() *ast.FuncDecl {
	return &ast.FuncDecl{
		Name: ast.NewIdent("New"),
		Type: &ast.FuncType{
			Params: &ast.FieldList{},
			Results: &ast.FieldList{
				List: []*ast.Field{
					{
						Type: &ast.StarExpr{
							X: ast.NewIdent("Handler"),
						},
					},
				},
			},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ReturnStmt{
					Results: []ast.Expr{
						&ast.UnaryExpr{
							Op: token.AND,
							X: &ast.CompositeLit{
								Type: ast.NewIdent("Handler"),
							},
						},
					},
				},
			},
		},
	}
}

// createHandleMethod creates the Handle method for the Handler struct based on the handler signature
func createHandleMethod(handlerFuncName, contextAlias, httpAlias, ioAlias string, handlerSig *HandlerSignature) *ast.FuncDecl {
	// Build the body statements
	var stmts []ast.Stmt

	// Read request body if handler expects input
	if handlerSig.HasInput {
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent("body"), ast.NewIdent("_")},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{
				&ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   ast.NewIdent(ioAlias),
						Sel: ast.NewIdent("ReadAll"),
					},
					Args: []ast.Expr{
						&ast.SelectorExpr{
							X:   ast.NewIdent("r"),
							Sel: ast.NewIdent("Body"),
						},
					},
				},
			},
		})
	}

	// Build handler call arguments
	var handlerArgs []ast.Expr
	if handlerSig.HasContext {
		handlerArgs = append(handlerArgs, ast.NewIdent("ctx"))
	}
	if handlerSig.HasInput {
		handlerArgs = append(handlerArgs, ast.NewIdent("body"))
	}

	// Call the handler and capture results
	if handlerSig.HasOutput && handlerSig.HasError {
		// result, err := handlerFuncName(args...)
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent("result"), ast.NewIdent("err")},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{
				&ast.CallExpr{
					Fun:  ast.NewIdent(handlerFuncName),
					Args: handlerArgs,
				},
			},
		})
	} else if handlerSig.HasError {
		// err := handlerFuncName(args...)
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent("err")},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{
				&ast.CallExpr{
					Fun:  ast.NewIdent(handlerFuncName),
					Args: handlerArgs,
				},
			},
		})
	} else if handlerSig.HasOutput {
		// result := handlerFuncName(args...)
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent("result")},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{
				&ast.CallExpr{
					Fun:  ast.NewIdent(handlerFuncName),
					Args: handlerArgs,
				},
			},
		})
	} else {
		// handlerFuncName(args...)
		stmts = append(stmts, &ast.ExprStmt{
			X: &ast.CallExpr{
				Fun:  ast.NewIdent(handlerFuncName),
				Args: handlerArgs,
			},
		})
	}

	// Handle error if handler returns one
	if handlerSig.HasError {
		// if err != nil {
		//     log.Printf("Handler error: %v", err)
		//     w.WriteHeader(500)
		//     return
		// }
		stmts = append(stmts, &ast.IfStmt{
			Cond: &ast.BinaryExpr{
				X:  ast.NewIdent("err"),
				Op: token.NEQ,
				Y:  ast.NewIdent("nil"),
			},
			Body: &ast.BlockStmt{
				List: []ast.Stmt{
					&ast.ExprStmt{
						X: &ast.CallExpr{
							Fun: &ast.SelectorExpr{
								X:   ast.NewIdent("log"),
								Sel: ast.NewIdent("Printf"),
							},
							Args: []ast.Expr{
								&ast.BasicLit{
									Kind:  token.STRING,
									Value: `"Handler error: %v"`,
								},
								ast.NewIdent("err"),
							},
						},
					},
					&ast.ExprStmt{
						X: &ast.CallExpr{
							Fun: &ast.SelectorExpr{
								X:   ast.NewIdent("w"),
								Sel: ast.NewIdent("WriteHeader"),
							},
							Args: []ast.Expr{
								&ast.BasicLit{
									Kind:  token.INT,
									Value: "500",
								},
							},
						},
					},
					&ast.ReturnStmt{},
				},
			},
		})
	}

	// Handle output if handler returns one
	if handlerSig.HasOutput {
		// json.NewEncoder(w).Encode(result)
		stmts = append(stmts, &ast.ExprStmt{
			X: &ast.CallExpr{
				Fun: &ast.SelectorExpr{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   ast.NewIdent("json"),
							Sel: ast.NewIdent("NewEncoder"),
						},
						Args: []ast.Expr{ast.NewIdent("w")},
					},
					Sel: ast.NewIdent("Encode"),
				},
				Args: []ast.Expr{ast.NewIdent("result")},
			},
		})
	}

	return &ast.FuncDecl{
		Recv: &ast.FieldList{
			List: []*ast.Field{
				{
					Names: []*ast.Ident{ast.NewIdent("h")},
					Type: &ast.StarExpr{
						X: ast.NewIdent("Handler"),
					},
				},
			},
		},
		Name: ast.NewIdent("Handle"),
		Type: &ast.FuncType{
			Params: &ast.FieldList{
				List: []*ast.Field{
					{
						Names: []*ast.Ident{ast.NewIdent("ctx")},
						Type: &ast.SelectorExpr{
							X:   ast.NewIdent(contextAlias),
							Sel: ast.NewIdent("Context"),
						},
					},
					{
						Names: []*ast.Ident{ast.NewIdent("w")},
						Type: &ast.SelectorExpr{
							X:   ast.NewIdent(httpAlias),
							Sel: ast.NewIdent("ResponseWriter"),
						},
					},
					{
						Names: []*ast.Ident{ast.NewIdent("r")},
						Type: &ast.StarExpr{
							X: &ast.SelectorExpr{
								X:   ast.NewIdent(httpAlias),
								Sel: ast.NewIdent("Request"),
							},
						},
					},
				},
			},
		},
		Body: &ast.BlockStmt{
			List: stmts,
		},
	}
}
