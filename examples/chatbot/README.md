# chatbot example

Integrates with the Grok API to provide a chatbot that can chat with the Minecraft server.

NB put your Grok API key in the `.envrc` file (`export GROK_API_KEY="gsk-your-api-key"`). Get API key from [console.groq.com/keys](https://console.groq.com/keys)

Then, run the bot with:

```bash
go run main.go -s [server_address (default is localhost:25565)]
```
