package gpt

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

type GPT struct {
	client *openai.Client
	model  string
}

func Init(apiKey string) *GPT {
	gpt := GPT{}

	gpt.client = openai.NewClient(apiKey)
	gpt.model = openai.GPT3Dot5Turbo
	return &gpt
}

func (gpt *GPT) Invoke(msgs []string) (string, error) {
	var messageText string

	messages := make([]openai.ChatCompletionMessage, 0)

	if len(msgs) > 1 {
		for idx, msg := range msgs {
			if idx == len(msgs)-1 {
				break
			}
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: msg,
			})
		}
	}

	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: msgs[len(msgs)-1],
	})

	resp, err := gpt.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:    gpt.model,
			Messages: messages,
		},
	)

	if err != nil {
		return "", fmt.Errorf("ChatCompletion error: %v", err)
	}

	messageText = resp.Choices[0].Message.Content
	return messageText, nil
}
