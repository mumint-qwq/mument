import { beforeEach, describe, expect, it, vi } from 'vitest';
let preferences;
beforeEach(async()=>{localStorage.clear();vi.resetModules();preferences=await import('./mosaicImageSource.js');});
describe('client-local mosaic image source',()=>{
  it('defaults to thumbnails and persists an original-image selection',async()=>{
    expect(preferences.getMosaicImageSource('alice')).toBe('thumbnail');
    preferences.setMosaicImageSource('alice','original');
    expect(localStorage.getItem('quantumMosaicImageSource:alice')).toBe('original');
    vi.resetModules();const reloaded=await import('./mosaicImageSource.js');
    expect(reloaded.getMosaicImageSource('alice')).toBe('original');
  });
  it('isolates accounts and validates stored values',()=>{
    preferences.setMosaicImageSource('alice','original');
    expect(preferences.getMosaicImageSource('bob')).toBe('thumbnail');
    preferences.setMosaicImageSource('alice','invalid');
    expect(preferences.getMosaicImageSource('alice')).toBe('thumbnail');
    localStorage.setItem('quantumMosaicImageSource:charlie','invalid');
    expect(preferences.getMosaicImageSource('charlie')).toBe('thumbnail');
  });
  it('updates live when another tab changes the choice',()=>{
    expect(preferences.getMosaicImageSource('alice')).toBe('thumbnail');
    window.dispatchEvent(new StorageEvent('storage',{key:'quantumMosaicImageSource:alice',newValue:'original'}));
    expect(preferences.getMosaicImageSource('alice')).toBe('original');
  });
});
