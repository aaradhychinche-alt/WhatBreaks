require("dotenv").config();
const express = require("express");
const cors = require("cors");
const helmet = require("helmet");

const { logger, resolveClientIp } = require("./utils/logger.js");

const app = express();

const APP_URL = process.env.APP_URL || "http://localhost:5173";

// 1. Helmet - Enhanced Security headers
app.use(
  helmet({
    contentSecurityPolicy: {
      directives: {
        defaultSrc: ["'self'"],
        styleSrc: ["'self'", "'unsafe-inline'"],
        scriptSrc: ["'self'"],
        imgSrc: ["'self'", "data:", "https:"],
        connectSrc: ["'self'", APP_URL],
        fontSrc: ["'self'"],
        objectSrc: ["'none'"],
        mediaSrc: ["'self'"],
        frameSrc: ["'none'"],
        baseUri: ["'self'"],
        formAction: ["'self'"],
        frameAncestors: ["'none'"], // Prevent clickjacking
      },
    },
    // Enhanced security headers
    hsts: {
      maxAge: 31536000, // 1 year
      includeSubDomains: true,
      preload: true,
    },
    noSniff: true,
    frameguard: { action: "deny" },
    xssFilter: true,
    referrerPolicy: { policy: "strict-origin-when-cross-origin" },
    crossOriginEmbedderPolicy: false,
    crossOriginResourcePolicy: { policy: "cross-origin" },
  }),
);

// 2. CORS setup
const corsOptions = {
  origin: APP_URL, // In production, this should be configurable
  credentials: true,
  methods: ["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"],
  allowedHeaders: [
    "Content-Type",
    "Authorization",
    "X-Requested-With",
    "X-CSRF-Token",
    "Idempotency-Key",
  ],
  maxAge: 86400, // 24 hours
};
app.use(cors(corsOptions));
app.options(/.*/, cors(corsOptions));

app.use(express.json({ limit: "10mb" }));
app.use(express.urlencoded({ extended: true, limit: "10mb" }));

// Rate limiters
const {
  speedLimiter,
  applyGlobalRateLimit,
} = require("./middleware/rateLimit");

app.use(applyGlobalRateLimit);
if (process.env.NODE_ENV !== "development" && process.env.NODE_ENV !== "test") {
  app.use(speedLimiter);
}

// --- HEALTH ROUTE ---
app.use(require("./routes/health"));

// --- WHATBREAKS ROUTES HERE ---

/**
 * Centralized error handling middleware
 */
const errorHandler = (err, req, res, next) => {
  if (res.headersSent) {
    return next(err);
  }
  logger.error("Unhandled error:", {
    error: err.message,
    stack: err.stack,
    url: req.url,
    method: req.method,
    ip: resolveClientIp(req),
  });

  const isDevelopment = process.env.NODE_ENV !== "production";

  res.status(err.status || 500).json({
    error: isDevelopment ? err.message : "Internal server error",
    code: err.code || "INTERNAL_ERROR",
    ...(isDevelopment && { stack: err.stack }),
  });
};

app.use(errorHandler);

// 404 handler (must be last)
app.use((req, res) => {
  res.status(404).json({
    error: "Endpoint not found",
    code: "NOT_FOUND",
  });
});

// --- START SERVER ---
const PORT = process.env.PORT || 4000;
const HOST = process.env.HOST || "0.0.0.0";

const server = app.listen(PORT, HOST, () => {
  logger.info(`Server running on http://${HOST}:${PORT}`);
  logger.info(`Environment: ${process.env.NODE_ENV || "development"}`);
});

// Graceful shutdown handling
process.on("SIGINT", () => {
  logger.info("SIGINT signal received: closing HTTP server");
  server.close(() => {
    logger.info("HTTP server closed");
    process.exit(0);
  });
});

process.on("SIGTERM", () => {
  logger.info("SIGTERM signal received: closing HTTP server");
  server.close(() => {
    logger.info("HTTP server closed");
    process.exit(0);
  });
});
