export function finishCheckout(payment: { confirmed: boolean }, clearCart: () => void) {
  // Preserve the cart after a failed or interrupted payment.
  if (payment.confirmed) clearCart();
}
