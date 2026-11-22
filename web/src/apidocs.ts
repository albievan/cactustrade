// Swagger UI for the embedded OpenAPI contract, bundled (no CDN). Served at /api/docs.
import SwaggerUIBundle from "swagger-ui-dist/swagger-ui-bundle";
import "swagger-ui-dist/swagger-ui.css";

SwaggerUIBundle({
  url: "/api/openapi.yaml",
  dom_id: "#swagger-ui",
  deepLinking: true,
  // Session-cookie calls from "Try it out" need the CSRF token: copy it from /api/v1/auth/me.
  requestInterceptor: (req: { headers: Record<string, string> }) => {
    const t = sessionStorage.getItem("cactustrade.csrf");
    if (t) req.headers["X-CSRF-Token"] = t;
    return req;
  },
});
