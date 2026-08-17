package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// CalculatorTool L0 纯计算工具：计算数学表达式。
//
// 与 framework/tool.CalculatorTool（a/b/op 二元运算）的区别：
// 本工具接收"整个表达式字符串"，对 LLM 更友好——模型把用户的问题直接
// 转成表达式即可，不必拆解成操作数与运算符（Function Calling 更易命中）。
type CalculatorTool struct{}

// calculatorArgs 计算器参数。
type calculatorArgs struct {
	Expression string `json:"expression"`
}

// Schema 实现 Tool 接口。
func (CalculatorTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "calculator",
		Description: "数学计算器：计算数学表达式并返回精确结果。支持四则运算 + - * /、取模 %、幂 ^（右结合）、括号与一元正负号，可用常量 pi、e。参数 expression 必填，写完整表达式，如 \"(2+3)*4-1\"、\"2^10\"、\"5%3\"。涉及任何数值计算时都应调用本工具保证精度。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"expression":{"type":"string","description":"要计算的数学表达式，如 (2+3)*4-1、2^10、sqrt 类不支持"}
			}
		}`),
		Required:   []string{"expression"},
		Permission: schema.PermissionL0Pure,
	}
}

// Execute 实现 Tool 接口。
func (CalculatorTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p calculatorArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("calculator: 参数解析失败: %w", err)
	}
	if strings.TrimSpace(p.Expression) == "" {
		return "", fmt.Errorf("calculator: expression 不能为空")
	}

	v, err := calcEval(p.Expression)
	if err != nil {
		return "", fmt.Errorf("calculator: 表达式 %q 计算失败: %v", p.Expression, err)
	}

	// 非有限结果（如 10^100000 溢出 float64）：直接报错，避免返回 +Inf。
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", fmt.Errorf("calculator: 结果超出可计算范围（数值过大溢出）")
	}

	out := strconv.FormatFloat(v, 'f', -1, 64)
	// 防止极端表达式（如 10^100000）撑爆上下文。
	if len(out) > 100 {
		return "", fmt.Errorf("calculator: 结果过大（%d 位），请简化表达式", len(out))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 表达式解析与求值：词法分析 + 递归下降（支持 + - * / % ^ 括号 一元负号）
// ---------------------------------------------------------------------------

type calcKind int

const (
	calcNum calcKind = iota
	calcPlus
	calcMinus
	calcStar
	calcSlash
	calcPercent
	calcCaret
	calcLParen
	calcRParen
	calcIdent
	calcEnd
)

type calcTok struct {
	kind calcKind
	text string
	num  float64
}

// calcEval 解析并计算表达式。
func calcEval(expr string) (float64, error) {
	toks, err := calcTokenize(expr)
	if err != nil {
		return 0, err
	}
	p := &calcParser{toks: toks}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	if p.peek().kind != calcEnd {
		return 0, fmt.Errorf("表达式末尾有多余内容 %q", p.peek().text)
	}
	return v, nil
}

func calcTokenize(s string) ([]calcTok, error) {
	var toks []calcTok
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '+':
			toks, i = append(toks, calcTok{kind: calcPlus, text: "+"}), i+1
		case ch == '-':
			toks, i = append(toks, calcTok{kind: calcMinus, text: "-"}), i+1
		case ch == '*':
			toks, i = append(toks, calcTok{kind: calcStar, text: "*"}), i+1
		case ch == '/':
			toks, i = append(toks, calcTok{kind: calcSlash, text: "/"}), i+1
		case ch == '%':
			toks, i = append(toks, calcTok{kind: calcPercent, text: "%"}), i+1
		case ch == '^':
			toks, i = append(toks, calcTok{kind: calcCaret, text: "^"}), i+1
		case ch == '(':
			toks, i = append(toks, calcTok{kind: calcLParen, text: "("}), i+1
		case ch == ')':
			toks, i = append(toks, calcTok{kind: calcRParen, text: ")"}), i+1
		case ch >= '0' && ch <= '9' || ch == '.':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			v, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("数字 %q 格式非法（位置 %d）", s[i:j], i)
			}
			toks = append(toks, calcTok{kind: calcNum, num: v, text: s[i:j]})
			i = j
		case ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z':
			j := i
			for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
				j++
			}
			toks = append(toks, calcTok{kind: calcIdent, text: s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("无法识别的字符 %q（位置 %d）", string(ch), i)
		}
	}
	toks = append(toks, calcTok{kind: calcEnd})
	return toks, nil
}

// calcParser 递归下降解析器。
type calcParser struct {
	toks []calcTok
	pos  int
}

func (p *calcParser) peek() calcTok { return p.toks[p.pos] }
func (p *calcParser) next() calcTok { t := p.toks[p.pos]; p.pos++; return t }

// expr := term (('+'|'-') term)*
func (p *calcParser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for p.peek().kind == calcPlus || p.peek().kind == calcMinus {
		op := p.next().kind
		rhs, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == calcPlus {
			v += rhs
		} else {
			v -= rhs
		}
	}
	return v, nil
}

// term := power (('*'|'/'|'%') power)*
func (p *calcParser) parseTerm() (float64, error) {
	v, err := p.parsePower()
	if err != nil {
		return 0, err
	}
	for {
		op := p.peek().kind
		if op != calcStar && op != calcSlash && op != calcPercent {
			break
		}
		p.next()
		rhs, err := p.parsePower()
		if err != nil {
			return 0, err
		}
		switch op {
		case calcStar:
			v *= rhs
		case calcSlash:
			if rhs == 0 {
				return 0, fmt.Errorf("除数不能为 0")
			}
			v /= rhs
		case calcPercent:
			if rhs == 0 {
				return 0, fmt.Errorf("取模除数不能为 0")
			}
			v = math.Mod(v, rhs)
		}
	}
	return v, nil
}

// power := unary ('^' power)?   —— 幂右结合
func (p *calcParser) parsePower() (float64, error) {
	v, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	if p.peek().kind == calcCaret {
		p.next()
		rhs, err := p.parsePower()
		if err != nil {
			return 0, err
		}
		if v == 0 && rhs < 0 {
			return 0, fmt.Errorf("0 的负数次幂无意义")
		}
		v = math.Pow(v, rhs)
	}
	return v, nil
}

// unary := ('-'|'+') unary | primary
func (p *calcParser) parseUnary() (float64, error) {
	switch p.peek().kind {
	case calcMinus:
		p.next()
		v, err := p.parseUnary()
		return -v, err
	case calcPlus:
		p.next()
		return p.parseUnary()
	default:
		return p.parsePrimary()
	}
}

// primary := number | const | '(' expr ')'
func (p *calcParser) parsePrimary() (float64, error) {
	t := p.next()
	switch t.kind {
	case calcNum:
		return t.num, nil
	case calcIdent:
		switch t.text {
		case "pi":
			return math.Pi, nil
		case "e":
			return math.E, nil
		}
		return 0, fmt.Errorf("未知常量 %q（仅支持 pi、e）", t.text)
	case calcLParen:
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.next().kind != calcRParen {
			return 0, fmt.Errorf("缺少右括号")
		}
		return v, nil
	case calcEnd:
		return 0, fmt.Errorf("表达式不完整")
	default:
		return 0, fmt.Errorf("语法错误：意外的 %q", t.text)
	}
}

// 编译期断言：CalculatorTool 实现 Tool 接口。
var _ tool.Tool = CalculatorTool{}
