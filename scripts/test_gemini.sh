#!/bin/bash
curl -s "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-lite:generateContent?key=AIzaSyCHgecazPw0OtFxQHrXSMHfK0DUFJ95374" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"parts":[{"text":"Say hello in one word"}]}],"generationConfig":{"maxOutputTokens":10}}'
