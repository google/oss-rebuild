package parsing

import (
	"fmt"
	"strconv"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// SetupCall represents a single setup() function call
type setupCall struct {
	line           uint
	column         uint
	positionalArgs []extractedValue
	keywordArgs    map[string]extractedValue
}

type setupArguments struct {
	keywordArgs    map[string]extractedValue
	positionalArgs []extractedValue
}

type cleanedSetupCall struct {
	callNumber int
	location   string
	arguments  setupArguments
}

// AnalysisResult represents the complete analysis output
type analysisResult struct {
	analyzedFile string
	setupCalls   []cleanedSetupCall
}

// SetupAnalyzer analyzes Python code for setup() function calls
type setupAnalyzer struct {
	sourceCode []byte
	tree       *tree_sitter.Tree
	variables  map[string]extractedValue
	imports    map[string]string
	setupCalls []setupCall
}

type extractedValue struct {
	value interface{} // Has to be any type (interface) cause it can be anything. The exact (Python) type is added as a property here
	typ  string
}

// Analyze performs the full analysis
func (sa *setupAnalyzer) analyze() {
	rootNode := sa.tree.RootNode()
	// fmt.Println("Root Node Type:", rootNode.ToSexp())
	sa.traverseNode(rootNode, 0)
}

// traverseNode recursively traverses the AST
func (sa *setupAnalyzer) traverseNode(node *tree_sitter.Node, level int) {
	if node == nil {
		return
	}

	nodeType := node.GrammarName()
	// fmt.Println("Current Node:", node.Id(), "Type:", nodeType, "Level:", level)

	// GREG TODO - Need to check to make sure these are in the grammar
	switch nodeType {
	case "assignment":
		sa.handleAssignment(node)
	case "augmented_assignment":
		sa.handleAugmentedAssignment(node)
	case "import_statement", "import_from_statement":
		sa.handleImport(node)
	// Only goes through setup calls and only the arguments
	case "call":
		sa.handleCall(node)
	}

	// Recursively visit all children
	for i := uint(0); i < node.ChildCount(); i++ {
		sa.traverseNode(node.Child(i), level+1)
	}
}

// handleAssignment processes variable assignments
func (sa *setupAnalyzer) handleAssignment(node *tree_sitter.Node) {
	// Find the left side (target) and right side (value)
	var targetNode, valueNode *tree_sitter.Node

	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child.GrammarName() == "identifier" && targetNode == nil {
			targetNode = child
			// Handling typed inputs
		} else if child.GrammarName() != "=" && child.GrammarName() != ":" && valueNode == nil && targetNode != nil {
			valueNode = child
		}
	}

	if targetNode != nil && valueNode != nil {
		varName := sa.getNodeText(targetNode)
		value := sa.extractValue(valueNode, true)
		sa.variables[varName] = value
	}
}

// handleAugmentedAssignment processes augmented assignments (+=, etc.)
func (sa *setupAnalyzer) handleAugmentedAssignment(node *tree_sitter.Node) {
	// Find target and operator
	var targetNode, valueNode *tree_sitter.Node
	var operator string

	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		childType := child.GrammarName()

		if childType == "identifier" && targetNode == nil {
			targetNode = child
		} else if strings.HasSuffix(childType, "=") {
			operator = childType
		} else if targetNode != nil && valueNode == nil {
			valueNode = child
		}
	}

	if !(targetNode != nil && valueNode != nil && operator != "") {
		return
	}

	varName := sa.getNodeText(targetNode)
	if currentVariable, ok := sa.variables[varName]; !ok {
		newVariableValue := sa.handleSimpleOperations(currentVariable, sa.extractValue(valueNode, true), operator)
		sa.variables[varName] = newVariableValue
	}
}

// handleImport processes import statements
func (sa *setupAnalyzer) handleImport(node *tree_sitter.Node) {
	nodeType := node.GrammarName()

	switch nodeType {
	case "import_statement":
		// Handle: import module [as alias]
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() == "dotted_name" {
				moduleName := sa.getNodeText(child)
				sa.imports[moduleName] = moduleName
			} else if child.GrammarName() == "aliased_import" {
				// Get the actual name and alias
				name := ""
				alias := ""
				for j := uint(0); j < child.ChildCount(); j++ {
					subChild := child.Child(j)
					if subChild.GrammarName() == "dotted_name" {
						name = sa.getNodeText(subChild)
					} else if subChild.GrammarName() == "identifier" {
						alias = sa.getNodeText(subChild)
					}
				}
				if alias != "" {
					sa.imports[alias] = name
				} else {
					sa.imports[name] = name
				}
			}
		}
	case "import_from_statement":
		// Handle: from module import name [as alias]
		moduleName := ""
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() == "dotted_name" {
				moduleName = sa.getNodeText(child)
			} else if child.GrammarName() == "identifier" && moduleName == "" {
				moduleName = sa.getNodeText(child)
			}
		}

		// Find imported names
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() == "dotted_name" && moduleName != "" {
				importedName := sa.getNodeText(child)
				if importedName != moduleName {
					sa.imports[importedName] = moduleName + "." + importedName
				}
			} else if child.GrammarName() == "aliased_import" {
				name := ""
				alias := ""
				for j := uint(0); j < child.ChildCount(); j++ {
					subChild := child.Child(j)
					if subChild.GrammarName() == "identifier" && name == "" {
						name = sa.getNodeText(subChild)
					} else if subChild.GrammarName() == "identifier" {
						alias = sa.getNodeText(subChild)
					}
				}
				if moduleName != "" {
					if alias != "" {
						sa.imports[alias] = moduleName + "." + name
					} else {
						sa.imports[name] = moduleName + "." + name
					}
				}
			}
		}
	}
}

// handleCall processes function calls, looking for setup() calls
func (sa *setupAnalyzer) handleCall(node *tree_sitter.Node) {
	// Find the function being called
	var functionNode *tree_sitter.Node
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child.GrammarName() == "identifier" || child.GrammarName() == "attribute" {
			functionNode = child
			break
		}
	}

	if functionNode == nil {
		return
	}

	// Check if this is a setup() call
	funcName := sa.getNodeText(functionNode)
	isSetupCall := false

	if functionNode.GrammarName() == "identifier" && funcName == "setup" {
		isSetupCall = true
	} else if functionNode.GrammarName() == "attribute" {
		// Check if the attribute is "setup"
		for i := uint(0); i < functionNode.ChildCount(); i++ {
			child := functionNode.Child(i)
			if child.GrammarName() == "identifier" && sa.getNodeText(child) == "setup" {
				isSetupCall = true
				break
			}
		}
	}

	if !isSetupCall {
		return
	}

	// Extract arguments
	setupCall := setupCall{
		line:           node.StartPosition().Row + 1,
		column:         node.StartPosition().Column,
		positionalArgs: make([]extractedValue, 0),
		keywordArgs:    make(map[string]extractedValue),
	}

	// Find the argument_list node
	var argListNode *tree_sitter.Node
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child.GrammarName() == "argument_list" {
			argListNode = child
			break
		}
	}

	if argListNode != nil {
		for i := uint(0); i < argListNode.ChildCount(); i++ {
			child := argListNode.Child(i)
			childType := child.GrammarName()

			if childType == "keyword_argument" {
				// Extract keyword argument
				var keyNode, valueNode *tree_sitter.Node
				for j := uint(0); j < child.ChildCount(); j++ {
					subChild := child.Child(j)
					if subChild.GrammarName() == "identifier" && keyNode == nil {
						keyNode = subChild
					} else if subChild.GrammarName() != "=" && valueNode == nil && keyNode != nil {
						valueNode = subChild
					}
				}

				if keyNode != nil && valueNode != nil {
					key := sa.getNodeText(keyNode)
					value := sa.extractValue(valueNode, true)
					setupCall.keywordArgs[key] = value
				}
			} else if childType == "dictionary_splat" {
				// Handle **kwargs
				for j := uint(0); j < child.ChildCount(); j++ {
					subChild := child.Child(j)
					if subChild.GrammarName() != "**" {
						value := sa.extractValue(subChild, true)
						setupCall.keywordArgs["**kwargs"] = value
					}
				}
			} else if childType != "," && childType != "(" && childType != ")" && childType != "comment" {
				// Positional argument
				value := sa.extractValue(child, true)
				setupCall.positionalArgs = append(setupCall.positionalArgs, value)
			}
		}
	}

	sa.setupCalls = append(sa.setupCalls, setupCall)
}

// Not fully implemented yet
func (sa *setupAnalyzer) handleSimpleOperations(valueOne extractedValue, valueTwo extractedValue, operator string) extractedValue {
	valueOneValue := valueOne.value
	valueOneType := valueOne.typ

	valueTwoValue := valueTwo.value
	valueTwoType := valueTwo.typ

	// If things are not perfect, return a default string representation
	if valueOneValue == nil || valueTwoValue == nil || (valueOneType != "" && valueOneType == "defaultString") || (valueTwoType != "" && valueTwoType == "defaultString") {
		return extractedValue{
			value: fmt.Sprintf("<UnhandledExpression: %v %s %v>", valueOneValue, operator, valueTwoValue),
			typ:  "defaultString",
		}
	}

	resultValue := extractedValue{
		value: "UNHANDLED_FUNCTIONALITY",
		typ:  "defaultString",
	}

	// Handle list operations first since they are more complicated
	switch operator {
	case "+=":
		if valueOneType == "list" {
			if valueTwoType == "list" {
				resultValue.value = append(valueOneValue.([]extractedValue), valueTwoValue.([]extractedValue)...)
				resultValue.typ = "list"
			} else {
				resultValue.value = append(valueOneValue.([]extractedValue), valueTwo)
				resultValue.typ = "list"
			}
		} else {
			newValue := sa.handleSimpleOperations(valueOne, valueTwo, "+")
			resultValue = newValue
		}
	case "*=":
		if valueOneType == "list" && valueTwoType == "integer" {
			times := int(valueTwoValue.(int64))
			resultValue.value = make([]extractedValue, 0)
			for i := 0; i < times; i++ {
				resultValue.value = append(resultValue.value.([]extractedValue), valueOneValue.([]extractedValue)...)
			}
			resultValue.typ = "list"
		} else {
			newValue := sa.handleSimpleOperations(valueOne, valueTwo, "*")
			resultValue = newValue
		}
	case "-=":
		newValue := sa.handleSimpleOperations(valueOne, valueTwo, "-")
		resultValue = newValue
	case "/=":
		newValue := sa.handleSimpleOperations(valueOne, valueTwo, "/")
		resultValue = newValue
	case "+":
		// Handle addition for strings and numbers
		if valueOneType == "string" && valueTwoType == "string" {
			resultValue.value = valueOneValue.(string) + valueTwoValue.(string)
			resultValue.typ = "string"
		} else if valueOneType == "integer" && valueTwoType == "integer" {
			resultValue.value = valueOneValue.(int64) + valueTwoValue.(int64)
			resultValue.typ = "integer"
		} else if (valueOneType == "integer" && valueTwoType == "float") || (valueOneType == "float" && valueTwoType == "integer") {
			var val1, val2 float64
			if valueOneType == "integer" {
				val1 = float64(valueOneValue.(int64))
				val2 = valueTwoValue.(float64)
			} else {
				val1 = valueOneValue.(float64)
				val2 = float64(valueTwoValue.(int64))
			}
			resultValue.value = val1 + val2
			resultValue.typ = "float"
		} else if valueOneType == "list" && valueTwoType == "list" {
			resultValue.value = append(valueOneValue.([]extractedValue), valueTwoValue.([]extractedValue)...)
			resultValue.typ = "list"
		} else {
			resultValue.value = fmt.Sprintf("<UnhandledExpression: %v %s %v>", valueOneValue, operator, valueTwoValue)
			resultValue.typ = "defaultString"
		}
	case "-":
		// Handle subtraction for numbers
		if valueOneType == "integer" && valueTwoType == "integer" {
			resultValue.value = valueOneValue.(int64) - valueTwoValue.(int64)
			resultValue.typ = "integer"
		} else if valueOneType == "float" && valueTwoType == "float" {
			resultValue.value = valueOneValue.(float64) - valueTwoValue.(float64)
			resultValue.typ = "float"
		} else if (valueOneType == "integer" && valueTwoType == "float") || (valueOneType == "float" && valueTwoType == "integer") {
			var val1, val2 float64
			if valueOneType == "integer" {
				val1 = float64(valueOneValue.(int64))
				val2 = valueTwoValue.(float64)
			} else {
				val1 = valueOneValue.(float64)
				val2 = float64(valueTwoValue.(int64))
			}
			resultValue.value = val1 - val2
			resultValue.typ = "float"
		} else {
			resultValue.value = fmt.Sprintf("<UnhandledExpression: %v %s %v>", valueOneValue, operator, valueTwoValue)
			resultValue.typ = "defaultString"
		}
	case "*":
		// Handle multiplication for numbers
		if valueOneType == "integer" && valueTwoType == "integer" {
			resultValue.value = valueOneValue.(int64) * valueTwoValue.(int64)
			resultValue.typ = "integer"
		} else if valueOneType == "float" && valueTwoType == "float" {
			resultValue.value = valueOneValue.(float64) * valueTwoValue.(float64)
			resultValue.typ = "float"
		} else if (valueOneType == "integer" && valueTwoType == "float") || (valueOneType == "float" && valueTwoType == "integer") {
			var val1, val2 float64
			if valueOneType == "integer" {
				val1 = float64(valueOneValue.(int64))
				val2 = valueTwoValue.(float64)
			} else {
				val1 = valueOneValue.(float64)
				val2 = float64(valueTwoValue.(int64))
			}
			resultValue.value = val1 * val2
			resultValue.typ = "float"
		} else {
			// For unsupported operations, return a default string representation
			resultValue.value = fmt.Sprintf("<UnhandledExpression: %v %s %v>", valueOneValue, operator, valueTwoValue)
			resultValue.typ = "defaultString"
		}
	case "/":
		// Handle division for numbers
		if (valueOneType == "integer" || valueOneType == "float") && (valueTwoType == "integer" || valueTwoType == "float") {
			var val1, val2 float64
			if valueOneType == "integer" {
				val1 = float64(valueOneValue.(int64))
			} else {
				val1 = valueOneValue.(float64)
			}
			if valueTwoType == "integer" {
				val2 = float64(valueTwoValue.(int64))
			} else {
				val2 = valueTwoValue.(float64)
			}
			if val2 != 0 {
				resultValue.value = val1 / val2
				resultValue.typ = "float"
			} else {
				resultValue.value = "<DivisionByZero>"
				resultValue.typ = "defaultString"
			}
		}
		// Now the conditional operations (only doing it for the same type for now)
	case "==":
		if valueOneType == valueTwoType {
			resultValue.value = valueOneValue == valueTwoValue
			resultValue.typ = "boolean"
		}
	case "!=":
		if valueOneType == valueTwoType {
			resultValue.value = valueOneValue != valueTwoValue
			resultValue.typ = "boolean"
		}
	case "<":
		if valueOneType == valueTwoType {
			switch valueOneType {
			case "integer":
				resultValue.value = valueOneValue.(int64) < valueTwoValue.(int64)
				resultValue.typ = "boolean"
			case "float":
				resultValue.value = valueOneValue.(float64) < valueTwoValue.(float64)
				resultValue.typ = "boolean"
			case "string":
				resultValue.value = valueOneValue.(string) < valueTwoValue.(string)
				resultValue.typ = "boolean"
			}
		}
	case "<=":
		if valueOneType == valueTwoType {
			switch valueOneType {
			case "integer":
				resultValue.value = valueOneValue.(int64) <= valueTwoValue.(int64)
				resultValue.typ = "boolean"
			case "float":
				resultValue.value = valueOneValue.(float64) <= valueTwoValue.(float64)
				resultValue.typ = "boolean"
			case "string":
				resultValue.value = valueOneValue.(string) <= valueTwoValue.(string)
				resultValue.typ = "boolean"
			}
		}
	case ">":
		if valueOneType == valueTwoType {
			switch valueOneType {
			case "integer":
				resultValue.value = valueOneValue.(int64) > valueTwoValue.(int64)
				resultValue.typ = "boolean"
			case "float":
				resultValue.value = valueOneValue.(float64) > valueTwoValue.(float64)
				resultValue.typ = "boolean"
			case "string":
				resultValue.value = valueOneValue.(string) > valueTwoValue.(string)
				resultValue.typ = "boolean"
			}
		}
	case ">=":
		if valueOneType == valueTwoType {
			switch valueOneType {
			case "integer":
				resultValue.value = valueOneValue.(int64) >= valueTwoValue.(int64)
				resultValue.typ = "boolean"
			case "float":
				resultValue.value = valueOneValue.(float64) >= valueTwoValue.(float64)
				resultValue.typ = "boolean"
			case "string":
				resultValue.value = valueOneValue.(string) >= valueTwoValue.(string)
				resultValue.typ = "boolean"
			}
		}
	default:
		// For unsupported operations, return a default string representation
		resultValue.value = fmt.Sprintf("<UnhandledExpression: %v %s %v>", valueOneValue, operator, valueTwoValue)
		resultValue.typ = "defaultString"
	}

	return resultValue
}

// extractValue converts a tree-sitter node to a Go value
func (sa *setupAnalyzer) extractValue(node *tree_sitter.Node, resolveVars bool) extractedValue {
	var exVal extractedValue

	if node == nil {
		return exVal
	}

	nodeType := node.GrammarName()
	// nodeContent := sa.getNodeText(node)
	// fmt.Println(nodeContent)

	switch nodeType {
	case "string":
		// Extract string value, removing quotes
		text := sa.getNodeText(node)
		if len(text) >= 2 {
			// Remove quotes (handle ', ", ''', """)
			if strings.HasPrefix(text, `"""`) || strings.HasPrefix(text, `'''`) {
				if len(text) >= 6 {
					exVal.value = text[3 : len(text)-3]
					exVal.typ = "string"
					return exVal
				}
			} else if (strings.HasPrefix(text, `"`) && strings.HasSuffix(text, `"`)) ||
				(strings.HasPrefix(text, `'`) && strings.HasSuffix(text, `'`)) {
				exVal.value = text[1 : len(text)-1]
				exVal.typ = "string"
				return exVal
			}
		}
		exVal.value = text
		exVal.typ = "string"
		return exVal

	case "integer":
		text := sa.getNodeText(node)
		if val, err := strconv.ParseInt(text, 0, 64); err == nil {
			exVal.value = val
			exVal.typ = "integer"
			return exVal
		}
		exVal.value = text
		exVal.typ = "defaultString"
		return exVal

	case "float":
		text := sa.getNodeText(node)
		if val, err := strconv.ParseFloat(text, 64); err == nil {
			exVal.value = val
			exVal.typ = "float"
			return exVal
		}
		exVal.value = text
		exVal.typ = "defaultString"
		return exVal

	case "true":
		exVal.value = true
		exVal.typ = "boolean"
		return exVal

	case "false":
		exVal.value = false
		exVal.typ = "boolean"
		return exVal

	case "none":
		exVal.value = nil
		exVal.typ = "null"
		return exVal

	case "list":
		result := make([]extractedValue, 0)
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() != "[" && child.GrammarName() != "]" && child.GrammarName() != "," {
				result = append(result, sa.extractValue(child, resolveVars))
			}
		}
		exVal.value = result
		exVal.typ = "list"
		return exVal

	case "tuple":
		result := make([]extractedValue, 0)
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() != "(" && child.GrammarName() != ")" && child.GrammarName() != "," {
				result = append(result, sa.extractValue(child, resolveVars))
			}
		}
		exVal.value = result
		exVal.typ = "tuple"
		return exVal

	case "dictionary":
		result := make(map[string]extractedValue)
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() == "pair" {
				var keyNode, valueNode *tree_sitter.Node
				for j := uint(0); j < child.ChildCount(); j++ {
					subChild := child.Child(j)
					if subChild.GrammarName() != ":" && keyNode == nil {
						keyNode = subChild
					} else if subChild.GrammarName() != ":" && valueNode == nil && keyNode != nil {
						valueNode = subChild
					}
				}
				if keyNode != nil && valueNode != nil {
					keyObj := sa.extractValue(keyNode, resolveVars)
					var key string
					if keyObj.typ != "string" && keyObj.typ != "integer" {
						key = fmt.Sprintf("<unhandled_key_type: %v>", keyObj.value)
					} else {
						key = fmt.Sprintf("%s", keyObj.value)
					}
					value := sa.extractValue(valueNode, resolveVars)
					result[key] = value
				}
			}
		}
		exVal.value = result
		exVal.typ = "dictionary"
		return exVal

	case "subscript":
		// Handle indexing like var[0]
		var valueNode, indexNode *tree_sitter.Node
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() != "[" && child.GrammarName() != "]" && valueNode == nil {
				valueNode = child
			} else if child.GrammarName() != "[" && child.GrammarName() != "]" && valueNode != nil && indexNode == nil {
				indexNode = child
			}
		}
		if valueNode != nil && indexNode != nil {
			value := sa.extractValue(valueNode, resolveVars)
			index := sa.extractValue(indexNode, resolveVars)

			// Only handling simple cases where value is a list and index is an integer
			if value.typ == "list" && index.typ == "integer" {
				listVal := value.value.([]extractedValue)
				idx := int(index.value.(int64))
				if idx >= 0 && idx < len(listVal) {
					return listVal[idx]
				}
			} else if value.typ == "dictionary" && index.typ == "string" {
				dictVal := value.value.(map[string]extractedValue)
				if val, ok := dictVal[index.value.(string)]; ok {
					return val
				}
			} else if value.typ == "dictionary" && index.typ == "integer" {
				// Handle integer keys in dictionaries
				dictVal := value.value.(map[string]extractedValue)
				intKey := fmt.Sprintf("%d", index.value.(int64))
				if val, ok := dictVal[intKey]; ok {
					return val
				}
			} else if value.typ == "defaultString" {
				exVal.value = fmt.Sprintf("<subscripted_value: %s[%v]>", value.value, index.value)
				exVal.typ = "defaultString"
				return exVal
			}
		}

	case "identifier":
		varName := sa.getNodeText(node)
		if resolveVars {
			if val, ok := sa.variables[varName]; ok {
				return val
			}
		}
		exVal.value = fmt.Sprintf("<variable: %s>", varName)
		exVal.typ = "defaultString"
		return exVal

	case "attribute":
		// Handle attribute access like obj.attr
		parts := make([]string, 0)
		sa.collectAttributeParts(node, &parts)
		exVal.value = strings.Join(parts, ".")
		exVal.typ = "defaultString"
		return exVal

	case "call":
		// Handle function calls
		funcName := ""
		args := make([]string, 0)

		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() == "identifier" || child.GrammarName() == "attribute" {
				funcName = sa.getNodeText(child)
			} else if child.GrammarName() == "argument_list" {
				// Extract first few arguments for preview
				argCount := 0
				for j := uint(0); j < child.ChildCount() && argCount < 2; j++ {
					argChild := child.Child(j)
					if argChild.GrammarName() != "(" && argChild.GrammarName() != ")" && argChild.GrammarName() != "," {
						argVal := sa.extractValue(argChild, resolveVars)
						args = append(args, fmt.Sprintf("%v", argVal))
						argCount++
					}
				}
			}
		}

		argsStr := strings.Join(args, ", ")
		if len(args) >= 2 {
			argsStr += ", ..."
		}
		exVal.value = fmt.Sprintf("<function_call: %s(%s)>", funcName, argsStr)
		exVal.typ = "defaultString"
		return exVal

	case "binary_operator":
		// Handle operations like a + b
		var left, right *tree_sitter.Node
		var op string

		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			childType := child.GrammarName()

			if left == nil {
				left = child
			} else if isOperator(childType) {
				op = childType
			} else if right == nil {
				right = child
			}
		}

		if left != nil && right != nil && op != "" {
			leftVal := sa.extractValue(left, resolveVars)
			rightVal := sa.extractValue(right, resolveVars)
			return sa.handleSimpleOperations(leftVal, rightVal, op)
		}

	case "comparison_operator":
		// Handle comparisons like a < b
		// Only handling simple two-part comparisons for now
		var operator string
		left := extractedValue{
			value: "",
			typ:  "unsetString",
		}
		right := extractedValue{}
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if isOperator(child.GrammarName()) {
				operator = child.GrammarName()
			} else {
				val := sa.extractValue(child, resolveVars)
				if left.typ == "unsetString" {
					left = val
				} else {
					right = val
				}
			}
		}

		return sa.handleSimpleOperations(left, right, operator)

	case "boolean_operator":
		// Handle and/or operations
		// Only handling simple two-part operations for now
		var op string
		left := extractedValue{
			value: "",
			typ:  "unsetString",
		}
		right := extractedValue{}
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			childType := child.GrammarName()

			if childType == "and" || childType == "or" {
				op = childType
			} else {
				if left.typ == "unsetString" {
					left = sa.extractValue(child, resolveVars)
				} else {
					right = sa.extractValue(child, resolveVars)
				}
			}
		}

		return sa.handleSimpleOperations(left, right, op)

	case "unary_operator":
		// Handle unary operations like -x, not x
		var op string
		var operand *tree_sitter.Node

		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			childType := child.GrammarName()

			if isOperator(childType) || childType == "not" {
				op = childType
			} else {
				operand = child
			}
		}

		// Not really sure that this works...?
		if operand != nil && op != "" {
			return sa.extractValue(operand, resolveVars)
		}

	case "conditional_expression":
		// Handle ternary: a if condition else b
		var body, test, orelse *tree_sitter.Node

		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			childType := child.GrammarName()

			if childType == "if" || childType == "else" {
				continue
			} else if body == nil {
				body = child
			} else if test == nil {
				test = child
			} else if orelse == nil {
				orelse = child
			}
		}

		if body != nil && test != nil && orelse != nil {
			bodyVal := sa.extractValue(body, resolveVars)
			testVal := sa.extractValue(test, resolveVars)
			elseVal := sa.extractValue(orelse, resolveVars)
			if testVal.typ == "boolean" {
				if testVal.value.(bool) {
					return bodyVal
				} else {
					return elseVal
				}
			}
		}

	case "parenthesized_expression":
		// Just extract the inner expression
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child.GrammarName() != "(" && child.GrammarName() != ")" {
				return sa.extractValue(child, resolveVars)
			}
		}
	}

	// Default: return the text representation
	exVal.value = sa.getNodeText(node)
	exVal.typ = "defaultString"
	return exVal
}

// collectAttributeParts recursively collects parts of an attribute access
func (sa *setupAnalyzer) collectAttributeParts(node *tree_sitter.Node, parts *[]string) {
	if node == nil {
		return
	}

	if node.GrammarName() == "identifier" {
		*parts = append(*parts, sa.getNodeText(node))
		return
	}

	// For attribute nodes, recursively process
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child.GrammarName() != "." {
			sa.collectAttributeParts(child, parts)
		}
	}
}

// isOperator checks if a node type represents an operator
func isOperator(nodeType string) bool {
	operators := map[string]bool{
		"+": true, "-": true, "*": true, "/": true, "%": true, "**": true, "//": true,
		"<": true, "<=": true, ">": true, ">=": true, "==": true, "!=": true,
		"<<": true, ">>": true, "&": true, "|": true, "^": true, "~": true,
		"is": true, "in": true, "not": true, "and": true, "or": true,
	}
	return operators[nodeType]
}

// getNodeText extracts the text content of a node
func (sa *setupAnalyzer) getNodeText(node *tree_sitter.Node) string {
	if node == nil {
		return ""
	}
	startByte := node.StartByte()
	endByte := node.EndByte()
	if endByte > uint(len(sa.sourceCode)) {
		endByte = uint(len(sa.sourceCode))
	}
	return string(sa.sourceCode[startByte:endByte])
}

// GetResult returns the analysis result in the expected format
func (sa *setupAnalyzer) getResult(filename string) analysisResult {
	setupCallsSummary := make([]cleanedSetupCall, 0)

	for i, call := range sa.setupCalls {
		callSummary := cleanedSetupCall{
			callNumber: i + 1,
			location:   fmt.Sprintf("Line %d, Column %d", call.line, call.column),
			arguments: setupArguments{
				keywordArgs:    call.keywordArgs,
				positionalArgs: call.positionalArgs,
			},
		}

		setupCallsSummary = append(setupCallsSummary, callSummary)
	}

	return analysisResult{
		analyzedFile: filename,
		setupCalls:   setupCallsSummary,
	}
}

func gatherSetupPyData(filename string, sourceCode []byte) analysisResult {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(tree_sitter.NewLanguage(tree_sitter_python.Language()))

	tree := parser.Parse(sourceCode, nil)
	defer tree.Close()

	analyzer := &setupAnalyzer{
		sourceCode: sourceCode,
		tree:       tree,
		variables:  make(map[string]extractedValue),
		imports:    make(map[string]string),
		setupCalls: make([]setupCall, 0),
	}

	analyzer.analyze()

	// Get results
	result := analyzer.getResult(filename)
	return result
}
