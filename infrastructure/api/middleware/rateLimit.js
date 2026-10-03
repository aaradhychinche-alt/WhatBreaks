const rateLimit = require("express-rate-limit");
const { ipKeyGenerator } = require("express-rate-limit");
const slowDown = require("express-slow-down");
const { logger, resolveClientIp } = require("../utils/logger");

function intEnv(name, fallback) {
  const raw = process.env[name];
  if (raw === undefined || raw === null || raw === "") return fallback;
  const parsed = Number.parseInt(String(raw), 10);
  return Number.isFinite(parsed) ? parsed : fallback;
}

const isDevOrTest =
  process.env.NODE_ENV === "development" || process.env.NODE_ENV === "test";

const globalLimiter = rateLimit({
  windowMs: intEnv("GLOBAL_RATE_LIMIT_WINDOW_MS", 1 * 60 * 1000),
  max: intEnv("GLOBAL_RATE_LIMIT_MAX", isDevOrTest ? 1000 : 300),
  message: "Too many requests from this IP, please try again later.",
  standardHeaders: true,
  legacyHeaders: false,
  skipSuccessfulRequests: isDevOrTest,
  skipFailedRequests: false,
  keyGenerator: (req) => ipKeyGenerator(req),
});

const speedLimiter = slowDown({
  windowMs: intEnv("GLOBAL_SLOWDOWN_WINDOW_MS", 15 * 60 * 1000),
  delayAfter: intEnv("GLOBAL_SLOWDOWN_DELAY_AFTER", 50),
  delayMs: () => intEnv("GLOBAL_SLOWDOWN_DELAY_MS", 500),
  validate: { delayMs: false },
});

function createApiLimiter(max, label) {
  return rateLimit({
    windowMs: intEnv("API_RATE_LIMIT_WINDOW_MS", 15 * 60 * 1000),
    max,
    message: "Too many API requests, please try again later.",
    standardHeaders: true,
    legacyHeaders: false,
    skipSuccessfulRequests: false,
    keyGenerator: (req) => {
      if (req.user && req.user.id) {
        logger.info("Rate limiting key generated", {
          type: `api_user_${label}`,
          userId: req.user.id,
        });
        return `user-${req.user.id}`;
      }
      const ip = resolveClientIp(req) || ipKeyGenerator(req);
      logger.info("Rate limiting key generated", {
        type: `api_ip_${label}`,
        ip,
      });
      return ip;
    },
    handler: (req, res) => {
      logger.warn("RATE_LIMIT_EXCEEDED", {
        type: req.user && req.user.id ? `api_user_${label}` : `api_ip_${label}`,
        userId: req.user?.id,
        ip: resolveClientIp(req),
        userAgent: req.get("User-Agent"),
      });
      res.status(429).json({
        error: "Too many API requests, please wait a few seconds and try again.",
      });
    },
  });
}

const defaultApiLimiter = createApiLimiter(6000, "oss");

function getApiLimiter() {
  return defaultApiLimiter;
}

const noRateLimit = (req, res, next) => next();

function applyGlobalRateLimit(req, res, next) {
  if (req.path === "/api/session" || req.path === "/api/csrf-token")
    return next();
  if (req.path.startsWith("/api/") && req.user && req.user.id) return next();
  if (
    (process.env.NODE_ENV === "development" ||
      process.env.NODE_ENV === "test") &&
    req.path.startsWith("/api/")
  )
    return next();
  return globalLimiter(req, res, next);
}

module.exports = {
  globalLimiter,
  speedLimiter,
  getApiLimiter,
  noRateLimit,
  applyGlobalRateLimit,
};
