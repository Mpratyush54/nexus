import { useState } from "react";
import { X } from "lucide-react";
import { AccessibleModal } from "@/components/ui/AccessibleModal";

export type ChatImage = { url: string; name: string };

/** Thumbnails that open full size; the browser decodes them lazily. */
export function ChatImages({ images }: { images: ChatImage[] }) {
  const [open, setOpen] = useState<ChatImage | null>(null);
  return (
    <>
      <ul className="chat-images" aria-label="Images">
        {images.map((image) => (
          <li key={image.url}>
            <button
              type="button"
              onClick={() => setOpen(image)}
              aria-label={`Open ${image.name}`}
              title={image.name}
            >
              <img src={image.url} alt={image.name} loading="lazy" decoding="async" />
            </button>
          </li>
        ))}
      </ul>
      {open && (
        <AccessibleModal
          labelledBy="chat-image-title"
          className="chat-modal chat-lightbox"
          onClose={() => setOpen(null)}
        >
          <header>
            <h3 id="chat-image-title">{open.name}</h3>
            <button aria-label="Close image" onClick={() => setOpen(null)}>
              <X size={18} />
            </button>
          </header>
          <img src={open.url} alt={open.name} decoding="async" />
        </AccessibleModal>
      )}
    </>
  );
}
