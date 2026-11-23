import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import "bootstrap/dist/css/bootstrap.min.css";
import "bootstrap-icons/font/bootstrap-icons.min.css";
import "./styles.css";
import { App } from "./App";
import { SessionProvider } from "./lib/session";

// Bootstrap's light or dark theme follows the system's (as the other cactus apps).
const dark = window.matchMedia?.("(prefers-color-scheme: dark)");
const setTheme = () => document.documentElement.setAttribute("data-bs-theme", dark?.matches ? "dark" : "light");
setTheme();
dark?.addEventListener?.("change", setTheme);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <BrowserRouter>
      <SessionProvider>
        <App />
      </SessionProvider>
    </BrowserRouter>
  </StrictMode>,
);
