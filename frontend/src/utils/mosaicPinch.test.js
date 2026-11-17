import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import { attachMosaicPinch, isMosaicInteractionBlocked } from './mosaicPinch.js';

const touch = (identifier, clientX, clientY=100) => ({identifier,clientX,clientY});
function dispatch(target,type,touches) {
  const event=new Event(type,{bubbles:true,cancelable:true});
  Object.defineProperty(event,'touches',{value:touches});
  target.dispatchEvent(event);
  return event;
}
let root,tile,handlers,control;
beforeEach(()=>{
  document.body.innerHTML='<div class="listing-view-root"><div class="listing-items mosaic"><a class="listing-item"></a></div></div>';
  root=document.querySelector('.listing-view-root');tile=root.querySelector('a');
  handlers={isEnabled:()=>true,getDensity:()=>0.5,getWidth:()=>390,onStart:vi.fn(),onPreview:vi.fn(),onCommit:vi.fn(),onCancel:vi.fn()};
  control=attachMosaicPinch(root,handlers);
});
afterEach(()=>{control.destroy();document.body.innerHTML='';});
describe('mosaic pinch input ownership',()=>{
  it('leaves a single finger untouched for native scrolling',()=>{
    expect(dispatch(tile,'touchstart',[touch(1,100)]).defaultPrevented).toBe(false);
    expect(dispatch(tile,'touchmove',[touch(1,100,200)]).defaultPrevented).toBe(false);
    dispatch(tile,'touchend',[]);
    expect(handlers.onStart).not.toHaveBeenCalled();
    expect(isMosaicInteractionBlocked(tile)).toBe(false);
  });
  it('commits a larger size at the endpoint and suppresses click side effects',()=>{
    const click=vi.fn();tile.addEventListener('click',click);
    expect(dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]).defaultPrevented).toBe(true);
    expect(isMosaicInteractionBlocked(tile)).toBe(true);
    dispatch(tile,'touchmove',[touch(1,0),touch(2,390)]);
    dispatch(tile,'touchend',[]);
    expect(handlers.onCommit).toHaveBeenCalledWith(1);
    expect(root.hasAttribute('data-mosaic-pinching')).toBe(false);
    tile.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true}));
    expect(click).not.toHaveBeenCalled();
  });
  it('keeps blocking the remaining finger until both have lifted',()=>{
    dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]);
    dispatch(tile,'touchend',[touch(1,100)]);
    expect(handlers.onCommit).toHaveBeenCalledOnce();
    expect(dispatch(tile,'touchmove',[touch(1,100,200)]).defaultPrevented).toBe(true);
    dispatch(tile,'touchend',[]);
    expect(dispatch(tile,'touchmove',[touch(1,100,220)]).defaultPrevented).toBe(false);
  });
  it('cancels instead of persisting a cancelled gesture',()=>{
    dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]);
    dispatch(tile,'touchmove',[touch(1,120),touch(2,180)]);
    dispatch(tile,'touchcancel',[]);
    expect(handlers.onCancel).toHaveBeenCalledOnce();
    expect(handlers.onCommit).not.toHaveBeenCalled();
  });
  it('cleans up pointer cancellation, window blur and event listeners',()=>{
    dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]);
    const event=new Event('pointercancel',{bubbles:true});
    Object.defineProperty(event,'pointerType',{value:'touch'});tile.dispatchEvent(event);
    expect(handlers.onCancel).toHaveBeenCalledOnce();
    dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]);
    window.dispatchEvent(new Event('blur'));
    expect(handlers.onCancel).toHaveBeenCalledTimes(2);
    control.destroy();
    expect(isMosaicInteractionBlocked(tile)).toBe(false);
    expect(dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]).defaultPrevented).toBe(false);
  });
  it('does not take gestures in another view or outside the listing',()=>{
    control.destroy();control=attachMosaicPinch(root,{...handlers,isEnabled:()=>false});
    expect(dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]).defaultPrevented).toBe(false);
    expect(handlers.onStart).not.toHaveBeenCalled();
  });
});

describe('additional pointer changes',()=>{
  it('does not jump to a different pair when a tracked finger is replaced by a third',()=>{
    dispatch(tile,'touchstart',[touch(1,100),touch(2,200)]);
    dispatch(tile,'touchstart',[touch(1,100),touch(2,200),touch(3,300)]);
    dispatch(tile,'touchend',[touch(2,200),touch(3,300)]);
    expect(handlers.onCancel).toHaveBeenCalledOnce();
    expect(handlers.onCommit).not.toHaveBeenCalled();
    expect(dispatch(tile,'touchmove',[touch(2,210),touch(3,290)]).defaultPrevented).toBe(true);
    dispatch(tile,'touchend',[]);
    expect(root.hasAttribute('data-mosaic-pinching')).toBe(false);
  });
});
