package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// ListGroqKeys handles GET /admin/ai/keys
func ListGroqKeys(c *fiber.Ctx) error {
	keys, err := services.ListGroqKeys()
	if err != nil {
		return utils.InternalError(c)
	}

	stats := services.GetKeyStats()

	return utils.Success(c, fiber.Map{
		"keys":  keys,
		"stats": stats,
	})
}

// AddGroqKey handles POST /admin/ai/keys
func AddGroqKey(c *fiber.Ctx) error {
	var req struct {
		Key      string `json:"key"`
		Label    string `json:"label"`
		KeyType  string `json:"key_type"` // whisper | llm | both
		Priority int    `json:"priority"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}

	if req.Key == "" {
		return utils.BadRequest(c, "API key is required")
	}
	if req.KeyType == "" {
		req.KeyType = "both"
	}

	key, err := services.AddGroqKey(req.Key, req.Label, req.KeyType, req.Priority)
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}

	return utils.Created(c, key)
}

// DeleteGroqKey handles DELETE /admin/ai/keys/:id
func DeleteGroqKey(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid key ID")
	}

	if err := services.DeleteGroqKey(id); err != nil {
		return utils.NotFound(c, err.Error())
	}

	return utils.SuccessMessage(c, "Key deleted")
}

// ToggleGroqKey handles POST /admin/ai/keys/:id/toggle
func ToggleGroqKey(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid key ID")
	}

	key, err := services.ToggleGroqKey(id)
	if err != nil {
		return utils.NotFound(c, "Key not found")
	}

	return utils.Success(c, key)
}

// ResetGroqKeyLimit handles POST /admin/ai/keys/:id/reset
func ResetGroqKeyLimit(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid key ID")
	}

	if err := services.ResetKeyRateLimit(id); err != nil {
		return utils.InternalError(c)
	}

	return utils.SuccessMessage(c, "Rate limit reset")
}

// TestGroqKey handles POST /admin/ai/keys/test
// Tests a key by making a real Groq API call.
func TestGroqKey(c *fiber.Ctx) error {
	var req struct {
		Key string `json:"key"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}

	if req.Key == "" {
		return utils.BadRequest(c, "Key is required")
	}

	// Test with a simple LLM call
	body, _ := json.Marshal(map[string]interface{}{
		"model":      config.App.GroqLLMModel,
		"messages":   []map[string]string{{"role": "user", "content": "Say OK"}},
		"max_tokens": 50,
	})

	httpReq, _ := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer "+req.Key)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return utils.Success(c, fiber.Map{"working": false, "error": "Connection failed: " + err.Error()})
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 200 {
		return utils.Success(c, fiber.Map{
			"working": true,
			"status":  resp.StatusCode,
			"message": "Key is valid and working",
		})
	}

	return utils.Success(c, fiber.Map{
		"working": false,
		"status":  resp.StatusCode,
		"error":   string(respBody),
	})
}
