export class BridgeError extends Error {
  constructor(code, message, details = undefined) {
    super(message);
    this.name = "BridgeError";
    this.code = code;
    this.details = details;
  }
}

export function asRPCError(error) {
  return {
    code: error?.code || "INTERNAL_ERROR",
    message: String(error?.message || error),
    details: error?.details,
  };
}
