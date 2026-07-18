import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { App } from "./app/App"
import { httpAPI } from "./api/client"
import "@fontsource-variable/geist/wght.css"
import "./styles/tokens.css"
import "./styles/app.css"

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App api={httpAPI} />
  </StrictMode>,
)
