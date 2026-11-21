package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func safeTerminalText(s string) string {
	var out strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			fmt.Fprintf(&out, "\\u{%04X}", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func generateApprovalKey(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: memdock-server keygen PRIVATE_KEY_FILE (prints public key)")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	path := args[0]
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err := fmt.Fprintln(f, hex.EncodeToString(private)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	fmt.Printf("Approval public key (configure MEMDOCK_APPROVAL_PUBLIC_KEY): %s\n", hex.EncodeToString(public))
	fmt.Printf("Private key saved with mode 0600 to %s; keep it outside Agent-accessible accounts.\n", filepath.Clean(path))
	return nil
}

type approvalRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

func parseApprovalRequest(data []byte) (approvalRequest, []byte, error) {
	var request approvalRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, nil, fmt.Errorf("invalid approval request JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return request, nil, fmt.Errorf("approval request file must contain one JSON object")
	}
	if request.Method != http.MethodPost && request.Method != http.MethodPatch && request.Method != http.MethodDelete {
		return request, nil, fmt.Errorf("only POST, PATCH, and DELETE are approvable")
	}
	if !strings.HasPrefix(request.Path, "/api/v1/") || strings.ContainsAny(request.Path, "?#") || strings.Contains(request.Path, "..") {
		return request, nil, fmt.Errorf("path must be a local /api/v1/ path without query or fragment")
	}
	body := bytes.TrimSpace(request.Body)
	if len(body) == 0 || len(body) > 1<<20 || !json.Valid(body) {
		return request, nil, fmt.Errorf("approval request body must be valid JSON between 1 byte and 1 MiB")
	}
	return request, body, nil
}

func approveRequest(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: memdock-server approve-request REQUEST.json")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	request, body, err := parseApprovalRequest(data)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "memdock-approved-request-*.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return approveWrite([]string{request.Method, request.Path, path})
}

func approveWrite(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: memdock-server approve METHOD /api/v1/path BODY.json")
	}
	if args[0] != http.MethodPost && args[0] != http.MethodPatch && args[0] != http.MethodDelete {
		return fmt.Errorf("only POST, PATCH, and DELETE are approvable")
	}
	path := args[1]
	if !strings.HasPrefix(path, "/api/v1/") || strings.ContainsAny(path, "?#") || strings.Contains(path, "..") {
		return fmt.Errorf("path must be a local /api/v1/ path without query or fragment")
	}
	keyFile := os.Getenv("MEMDOCK_APPROVAL_PRIVATE_KEY_FILE")
	if keyFile == "" {
		return fmt.Errorf("MEMDOCK_APPROVAL_PRIVATE_KEY_FILE is required")
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("approval private key must not be accessible by group or other users (chmod 600)")
	}
	keyHex, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	privateKey, err := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid Ed25519 private key file")
	}
	body, err := os.ReadFile(args[2])
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body) > 1<<20 {
		return fmt.Errorf("request body must be between 1 byte and 1 MiB")
	}
	if !json.Valid(body) {
		return fmt.Errorf("request body must be valid JSON")
	}
	stdinInfo, err := os.Stdin.Stat()
	if err != nil || stdinInfo.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("approval requires an interactive terminal")
	}
	endpoint := strings.TrimRight(os.Getenv("MEMDOCK_URL"), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("MEMDOCK_URL must be a trusted HTTPS URL")
	}
	writeToken := os.Getenv("MEMDOCK_APPROVER_TOKEN")
	if writeToken == "" {
		writeToken = os.Getenv("MEMDOCK_WRITE_TOKEN")
	}
	if len(writeToken) < 32 {
		return fmt.Errorf("MEMDOCK_APPROVER_TOKEN or MEMDOCK_WRITE_TOKEN must contain at least 32 bytes")
	}
	fmt.Fprintf(os.Stderr, "Request: %s %s\nBody (terminal-safe; exact bytes will be signed): %s\nApprove this exact request? Type 'approve': ", args[0], safeTerminalText(path), safeTerminalText(string(body)))
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && len(answer) == 0 {
		return err
	}
	if strings.TrimSpace(answer) != "approve" {
		return fmt.Errorf("approval declined")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	nonceHex := hex.EncodeToString(nonce)
	message := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", timestamp, nonceHex, args[0], path, body)
	signature := "approval:" + timestamp + ":" + nonceHex + ":" + hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateKey), []byte(message)))
	req, err := http.NewRequest(args[0], endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+writeToken)
	req.Header.Set("X-MemDock-Write-Signature", signature)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	_, _ = os.Stdout.Write(response)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("write request failed: HTTP %s", resp.Status)
	}
	return nil
}
