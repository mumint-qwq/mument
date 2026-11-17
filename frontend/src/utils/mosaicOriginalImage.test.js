import { beforeEach, describe, expect, it, vi } from 'vitest';
vi.mock('@/api/viewToken.js',()=>({ensureViewToken:vi.fn()}));
vi.mock('@/api/resources',()=>({getRawViewURL:vi.fn(),getRawViewURLPublic:vi.fn()}));
vi.mock('./previewRequests.js',()=>({fetchPreviewImage:vi.fn()}));
import {ensureViewToken} from '@/api/viewToken.js';
import {getRawViewURL,getRawViewURLPublic} from '@/api/resources';
import {fetchPreviewImage} from './previewRequests.js';
import {fetchMosaicOriginalImage} from './mosaicOriginalImage.js';
beforeEach(()=>{vi.resetAllMocks();ensureViewToken.mockResolvedValue('scoped-token');fetchPreviewImage.mockResolvedValue('blob:original');});
describe('original image view requests',()=>{
  it('uses scoped raw viewing and a modification key, not a preview or download endpoint',async()=>{
    getRawViewURL.mockReturnValue('/api/resources/view?file=a.jpg');
    const controller=new AbortController();
    expect(await fetchMosaicOriginalImage({source:'pictures',path:'/a.jpg',modified:'2026-10-24T00:00:00Z'},controller.signal)).toBe('blob:original');
    expect(getRawViewURL).toHaveBeenCalledWith('pictures','/a.jpg','scoped-token');
    expect(fetchPreviewImage).toHaveBeenCalledWith('/api/resources/view?file=a.jpg&key=1792800000000',controller.signal);
  });
  it('uses the public view endpoint for a share with the same view grant mechanism',async()=>{
    const share={hash:'fixture'};getRawViewURLPublic.mockReturnValue('/public/api/resources/view?file=a.jpg');
    await fetchMosaicOriginalImage({source:'pictures',path:'/a.jpg',share},new AbortController().signal);
    expect(getRawViewURLPublic).toHaveBeenCalledWith(share,['/a.jpg'],'scoped-token');
    expect(getRawViewURL).not.toHaveBeenCalled();
  });
  it('does not fetch file bytes without a view grant or after cancellation',async()=>{
    ensureViewToken.mockResolvedValue('');
    await expect(fetchMosaicOriginalImage({source:'pictures',path:'/a.jpg'},new AbortController().signal)).rejects.toThrow('View permission');
    ensureViewToken.mockResolvedValue('scoped-token');const controller=new AbortController();controller.abort();
    await expect(fetchMosaicOriginalImage({source:'pictures',path:'/a.jpg'},controller.signal)).rejects.toMatchObject({name:'AbortError'});
    expect(fetchPreviewImage).not.toHaveBeenCalled();
  });
});
