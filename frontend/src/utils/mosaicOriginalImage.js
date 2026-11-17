import { ensureViewToken } from '@/api/viewToken.js';
import { getRawViewURL, getRawViewURLPublic } from '@/api/resources';
import { fetchPreviewImage } from './previewRequests.js';

/** Read original bytes through the existing scoped view endpoint, never a download fallback. */
export async function fetchMosaicOriginalImage({ source, path, modified, share }, signal) {
  const token = await ensureViewToken(source);
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError');
  if (!token) throw new Error('View permission required');
  let url = share ? getRawViewURLPublic(share, [path], token) : getRawViewURL(source, path, token);
  const key = Date.parse(modified);
  if (Number.isFinite(key)) url += `&key=${key}`;
  return fetchPreviewImage(url, signal);
}
