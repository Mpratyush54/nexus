// Images a person attaches when replying from the web (📎, paste, camera
// roll). They travel to Nexus on their computer through the desktop relay,
// which checks them again: at most 4 images, 5 MB each, PNG/JPEG/GIF/WebP.

export const MAX_IMAGES = 4
export const MAX_IMAGE_BYTES = 5 * 1024 * 1024
/** Photos above this are shrunk in the browser before sending. */
export const SHRINK_ABOVE_BYTES = 1.5 * 1024 * 1024
export const MAX_SIDE = 2048
/** Relay version whose desktops accept images. */
export const IMAGE_PROTOCOL = 2
export const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'] as const
export const UPDATE_DESKTOP_MESSAGE = 'Update Nexus on your computer to send images.'

export type DraftImage = {
  id: string
  name: string
  mediaType: string
  /** base64 without the data: prefix */
  data: string
  size: number
  /** object URL for the thumbnail; revoke when removed */
  url: string
}

export function imageHarness(agent: string) {
  return agent === 'claude' || agent === 'codex' || agent === 'opencode'
}

/** Why images can't be attached right now, or '' when they can. */
export function attachBlocker(agent: string, protocol: number | undefined): string {
  if ((protocol ?? 1) < IMAGE_PROTOCOL) return UPDATE_DESKTOP_MESSAGE
  if (!imageHarness(agent)) return 'Images work with Claude Code, Codex, or OpenCode. Switch the agent to attach one.'
  return ''
}

/** Checks a picked or pasted file before reading it. */
export function fileProblem(file: { name: string; type: string; size: number }, alreadyAttached: number): string {
  const name = file.name || 'That file'
  if (alreadyAttached >= MAX_IMAGES) return `Attach at most ${MAX_IMAGES} images.`
  if (!(IMAGE_TYPES as readonly string[]).includes(file.type)) {
    if (file.type === 'image/heic' || file.type === 'image/heif' || /\.hei[cf]$/i.test(file.name))
      return `${name} is a HEIC photo. Choose “Most Compatible” in your camera settings, or share it as JPEG.`
    return `${name} isn't a PNG, JPEG, GIF, or WebP image.`
  }
  if (file.size === 0) return `${name} is empty.`
  if (file.type === 'image/gif' && file.size > MAX_IMAGE_BYTES) return `${name} is over 5 MB. GIFs can't be shrunk; choose a smaller one.`
  if (file.size > 40 * 1024 * 1024) return `${name} is too large. Choose an image under 40 MB.`
  return ''
}

/** Image files in a paste (screenshots, copied images). */
export function pastedImages(data: { files?: ArrayLike<File> | null; items?: ArrayLike<{ kind: string; type: string; getAsFile(): File | null }> | null } | null): File[] {
  if (!data) return []
  const out: File[] = []
  const files = data.files ? Array.from(data.files) : []
  for (const f of files) if (f.type.startsWith('image/')) out.push(f)
  if (out.length === 0 && data.items) {
    for (const item of Array.from(data.items)) {
      if (item.kind === 'file' && item.type.startsWith('image/')) {
        const f = item.getAsFile()
        if (f) out.push(f)
      }
    }
  }
  return out
}

export function fitWithin(width: number, height: number, max = MAX_SIDE) {
  const longest = Math.max(width, height)
  if (longest <= max || longest === 0) return { width, height }
  const scale = max / longest
  return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) }
}

/** Maps relay errors to what a person can do about them. */
export function relayErrorMessage(message: string): string {
  if (/unknown field "attachments"/i.test(message)) return 'This Nexus server can’t relay images yet. Send the message without images.'
  if (/update nexus on your computer/i.test(message)) return UPDATE_DESKTOP_MESSAGE
  if (/request body too large|http: request body/i.test(message)) return 'Those images are too large together. Remove one and try again.'
  return message
}

function readAsBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = String(reader.result || '')
      resolve(result.slice(result.indexOf(',') + 1))
    }
    reader.onerror = () => reject(reader.error ?? new Error('Could not read the image.'))
    reader.readAsDataURL(blob)
  })
}

async function shrink(file: File): Promise<Blob> {
  const bitmap = await createImageBitmap(file)
  try {
    for (const side of [MAX_SIDE, 1600, 1200]) {
      const { width, height } = fitWithin(bitmap.width, bitmap.height, side)
      const canvas = document.createElement('canvas')
      canvas.width = width
      canvas.height = height
      canvas.getContext('2d')?.drawImage(bitmap, 0, 0, width, height)
      const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/jpeg', 0.85))
      if (blob && blob.size <= MAX_IMAGE_BYTES) return blob
    }
  } finally {
    bitmap.close()
  }
  throw new Error(`${file.name} is still over 5 MB after resizing.`)
}

/** Reads one image for sending; big photos are resized to JPEG first. */
export async function prepareImage(file: File): Promise<DraftImage> {
  let blob: Blob = file
  let mediaType = file.type
  let name = file.name || 'image'
  if (file.type !== 'image/gif' && file.size > SHRINK_ABOVE_BYTES) {
    blob = await shrink(file)
    mediaType = 'image/jpeg'
    name = name.replace(/\.[^.]+$/, '') + '.jpg'
  }
  if (blob.size > MAX_IMAGE_BYTES) throw new Error(`${name} is over 5 MB.`)
  return {
    id: crypto.randomUUID(),
    name,
    mediaType,
    data: await readAsBase64(blob),
    size: blob.size,
    url: URL.createObjectURL(blob),
  }
}
