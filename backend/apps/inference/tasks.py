import requests
import logging
import base64
from io import BytesIO
from PIL import Image
from django.core.files.base import ContentFile
from backend.celery_app import app
from .models import InferenceRequest
import json
from celery import shared_task
import os
import time
from ..services.comfy_api import ComfyAPI

logger = logging.getLogger(__name__)

OPENAI_COMPATIBLE_API_URL = "http:/0.0.0.0:11434/v1"
NIM_FLUX_API_URL = "http://host.docker.internal:8001/v1"


def handle_image_generation(inference_request):
    """Handle image generation using the Flux API."""
    try:
        # Prepare the request to Flux API
        endpoint = f"{NIM_FLUX_API_URL}/infer"
        logger.info(
            f"Sending request to Flux API with payload: {inference_request.payload}"
        )

        response = requests.post(
            endpoint,
            json=inference_request.payload,
            headers={"Content-Type": "application/json"},
        )
        logger.info(
            f"Received response from Flux API with status code: {response.status_code}"
        )

        if response.status_code == 200:
            response_data = response.json()
            # Get the base64 image data from the response
            image_data = base64.b64decode(response_data["artifacts"][0]["base64"])

            # Create a PIL Image from the binary data
            image = Image.open(BytesIO(image_data))

            # Save the image to a BytesIO object
            image_io = BytesIO()
            image.save(image_io, format="PNG")
            image_io.seek(0)

            # Save the image to the model's generated_image field
            inference_request.generated_image.save(
                f"generated_image_{inference_request.id}.png",
                ContentFile(image_io.getvalue()),
                save=False,
            )

            # Store the full response in the response field
            inference_request.response = response_data
            inference_request.status = "completed"
            logger.info(
                f"Successfully processed image generation request {inference_request.id}"
            )
        else:
            inference_request.status = "failed"
            error_msg = f"Flux API request failed with status {response.status_code}: {response.text}"
            inference_request.error_details = error_msg
            logger.error(
                f"Failed to process image generation request {inference_request.id}: {error_msg}"
            )

        inference_request.save()
        return True

    except Exception as e:
        logger.error(
            f"Error in handle_image_generation for request {inference_request.id}: {str(e)}"
        )
        inference_request.status = "failed"
        inference_request.error_details = f"Image generation error: {str(e)}"
        inference_request.save()
        return False


@app.task
def process_inference_request(request_id):
    try:
        # Get the inference request
        inference_request = InferenceRequest.objects.get(id=request_id)
        logger.info(f"Processing inference request {request_id}")

        # Update status to in_progress
        inference_request.status = "in_progress"
        inference_request.save()
        logger.info(f"Updated status to in_progress for request {request_id}")

        if inference_request.inference_type == "image_generation":
            handle_image_generation(inference_request)
            return

        # Handle LLM requests
        endpoint = (
            f"{OPENAI_COMPATIBLE_API_URL}/chat/completions"
            if inference_request.inference_type == "llm_chat"
            else f"{OPENAI_COMPATIBLE_API_URL}/completions"
        )
        logger.info(f"Using endpoint: {endpoint}")

        # Make the API request
        logger.info(
            f"Sending request to Ollama with payload: {inference_request.payload}"
        )
        response = requests.post(
            endpoint,
            json=inference_request.payload,
            headers={"Content-Type": "application/json"},
        )
        logger.info(
            f"Received response from Ollama with status code: {response.status_code}"
        )

        # Handle the response
        if response.status_code == 200:
            inference_request.response = response.json()
            inference_request.status = "completed"
            logger.info(f"Successfully processed request {request_id}")
        else:
            inference_request.status = "failed"
            error_msg = f"API request failed with status {response.status_code}: {response.text}"
            inference_request.error_details = error_msg
            logger.error(f"Failed to process request {request_id}: {error_msg}")

        inference_request.save()

    except InferenceRequest.DoesNotExist:
        logger.error(f"Inference request {request_id} not found")
    except requests.exceptions.RequestException as e:
        logger.error(f"Request error for inference request {request_id}: {str(e)}")
        if inference_request:
            inference_request.status = "failed"
            inference_request.error_details = f"Request error: {str(e)}"
            inference_request.save()
    except Exception as e:
        logger.error(f"Unexpected error processing request {request_id}: {str(e)}")
        if inference_request:
            inference_request.status = "failed"
            inference_request.error_details = f"Unexpected error: {str(e)}"
            inference_request.save()


@app.task
def process_image_gen_inference_request(request_id):
    try:
        logger.info("🟢 [Celery] Starting process_image_gen_inference_request for ID %s", request_id)
        inference_request = InferenceRequest.objects.get(id=request_id)
        logger.info("🔍 [Celery] Loaded InferenceRequest %s", request_id)
        inference_request.status = "in_progress"
        inference_request.save()

        service = inference_request.image_gen_service
        if not service:
            logger.error("❌ [Celery] No image generation service associated with request %s", request_id)
            inference_request.status = "failed"
            inference_request.error_details = "No image generation service associated with this request."
            inference_request.save()
            return

        # Only support FLUX_NIM for now
        if service.service_type != "FLUX_NIM":
            logger.error("❌ [Celery] Service type %s not supported for request %s", service.service_type, request_id)
            inference_request.status = "failed"
            inference_request.error_details = f"Service type {service.service_type} not supported yet."
            inference_request.save()
            return

        # Always use /v1/infer path for Flux Nim
        if service.base_url.endswith('/'):
            endpoint = service.base_url.rstrip('/') + '/v1/infer'
        else:
            endpoint = service.base_url + '/v1/infer'
        payload = dict(inference_request.payload)
        payload.pop('ratio', None)  # Remove ratio if present
        # If mode is canny or depth and input_image is present, add it to the payload as 'image'
        mode = payload.get('mode')
        if mode in ('canny', 'depth') and inference_request.input_image:
            payload['image'] = inference_request.input_image
            # Flux Nim only accepts 'base' mode, but we'll keep the original mode in our payload
            # payload['mode'] = 'base'
            payload["preprocess_image"] = False
        # Log the full payload in a readable format
        logger.info("🌐 [Celery] Sending request to Flux Nim at %s", endpoint)
        logger.info("📦 [Celery] Full payload:\n%s", json.dumps(payload, indent=2))
        response = requests.post(
            endpoint,
            json=payload,
            headers={"Content-Type": "application/json", "accept": "application/json"},
        )
        logger.info("📬 [Celery] Received response from Flux Nim: %s", response.status_code)
        if response.status_code == 200:
            response_data = response.json()
            image_b64 = response_data["artifacts"][0]["base64"]
            image_data = base64.b64decode(image_b64)
            image = Image.open(BytesIO(image_data))
            image_io = BytesIO()
            image.save(image_io, format="PNG")
            image_io.seek(0)
            inference_request.generated_image.save(
                f"generated_image_{inference_request.id}.png",
                ContentFile(image_io.getvalue()),
                save=False,
            )
            inference_request.response = response_data
            inference_request.status = "completed"
            logger.info("✅ [Celery] Successfully processed image gen request %s", request_id)
        else:
            inference_request.status = "failed"
            error_msg = f"Flux Nim API request failed: {response.status_code} {response.text}"
            inference_request.error_details = error_msg
            logger.error("❌ [Celery] %s", error_msg)
        inference_request.save()
    except Exception as e:
        logger.error(f"🔥 [Celery] Error in process_image_gen_inference_request for request {request_id}: {str(e)}")
        if 'inference_request' in locals():
            inference_request.status = "failed"
            inference_request.error_details = f"Image generation error: {str(e)}"
            inference_request.save()
        return False


@app.task
def process_tts_inference_request(request_id):
    try:
        inference_request = InferenceRequest.objects.get(id=request_id)
        inference_request.status = "in_progress"
        inference_request.save()

        payload = inference_request.payload or {}
        text_input = payload.get("text_input", "")
        max_new_tokens = payload.get("max_new_tokens", 860)
        cfg_scale = payload.get("cfg_scale", 1)
        temperature = payload.get("temperature", 1)
        top_p = payload.get("top_p", 0.8)
        cfg_filter_top_k = payload.get("cfg_filter_top_k", 15)
        speed_factor = payload.get("speed_factor", 0.8)
        audio_prompt_input = None
        uploaded_file_path = None

        # Get the TTS service URL from the related service
        tts_service = inference_request.tts_service
        if not tts_service or not tts_service.url:
            logger.error("❌ No TTS service or URL associated with this request.")
            inference_request.status = "failed"
            inference_request.error_details = "No TTS service or URL associated with this request."
            inference_request.save()
            return
        base_url = tts_service.url.rstrip('/')
        upload_url = f"{base_url}/gradio_api/upload"
        generate_url = f"{base_url}/gradio_api/call/generate_audio"
        poll_url = lambda event_id: f"{base_url}/gradio_api/call/generate_audio/{event_id}"

        # If there is a conditioning audio file, upload it first
        if inference_request.speech_input:
            logger.info(f"📂 Uploading audio file: {inference_request.speech_input.path}")
            with inference_request.speech_input.open('rb') as audio_file:
                files = {
                    'files': (os.path.basename(inference_request.speech_input.name), audio_file, 'audio/wav')
                }
                upload_response = requests.post(
                    upload_url,
                    files=files
                )
                if upload_response.status_code != 200:
                    logger.error(f"❌ Failed to upload audio file. Status code: {upload_response.status_code}")
                    inference_request.status = "failed"
                    inference_request.error_details = f"Failed to upload audio file: {upload_response.text}"
                    inference_request.save()
                    return
                upload_data = upload_response.json()
                if not isinstance(upload_data, list) or len(upload_data) == 0:
                    logger.error("❌ Invalid response from upload endpoint")
                    inference_request.status = "failed"
                    inference_request.error_details = "Invalid upload response"
                    inference_request.save()
                    return
                uploaded_file_path = upload_data[0]
                logger.info(f"✅ Successfully uploaded audio file: {uploaded_file_path}")

        # Prepare the data for the TTS generation request
        data = [
            text_input,
            {
                "path": uploaded_file_path,
                "meta": {"_type": "gradio.FileData"}
            } if uploaded_file_path else None,
            max_new_tokens,
            cfg_scale,
            temperature,
            top_p,
            cfg_filter_top_k,
            speed_factor
        ]
        # Always send 8 arguments; if no audio, the second argument is None
        logger.info("📤 Sending TTS generation request...")
        response = requests.post(
            generate_url,
            headers={"Content-Type": "application/json"},
            json={"data": data}
        )
        logger.info(f"📥 Received response with status code: {response.status_code}")
        response_data = response.json()
        logger.info(f"📦 Response data: {json.dumps(response_data, indent=2)}")
        if "event_id" not in response_data:
            logger.error("❌ Response does not contain 'event_id' key.")
            inference_request.status = "failed"
            inference_request.error_details = f"No event_id in response: {response_data}"
            inference_request.save()
            return
        event_id = response_data["event_id"]
        logger.info(f"✅ Successfully received event ID: {event_id}")

        # Poll for the result
        logger.info("🎵 Requesting audio data...")
        audio_url = None
        audio_response = requests.get(
            poll_url(event_id),
            stream=True
        )
        if audio_response.status_code == 200:
            for line in audio_response.iter_lines():
                if line:
                    line = line.decode('utf-8')
                    logger.info(f"📝 Received event: {line}")
                    if line.startswith('data: '):
                        try:
                            data = json.loads(line[6:])
                            if isinstance(data, list) and len(data) > 0:
                                audio_data = data[0]
                                if isinstance(audio_data, dict) and 'url' in audio_data:
                                    audio_url = audio_data['url']
                                    logger.info(f"🎵 Found audio URL: {audio_url}")
                                    break
                        except json.JSONDecodeError:
                            continue
            if audio_url:
                logger.info(f"📥 Downloading audio from: {audio_url}")
                audio_file_response = requests.get(audio_url)
                if audio_file_response.status_code == 200:
                    filename = f"tts_output_{inference_request.id}.wav"
                    inference_request.speech_output.save(
                        filename,
                        ContentFile(audio_file_response.content),
                        save=False
                    )
                    inference_request.status = "completed"
                    inference_request.save()
                    logger.info(f"✨ Successfully saved audio to {filename}")
                    return
                else:
                    logger.error(f"❌ Failed to download audio file. Status code: {audio_file_response.status_code}")
                    inference_request.status = "failed"
                    inference_request.error_details = f"Failed to download audio file: {audio_file_response.text}"
                    inference_request.save()
                    return
            else:
                logger.error("❌ Could not find audio URL in the response")
                inference_request.status = "failed"
                inference_request.error_details = "Could not find audio URL in the response"
                inference_request.save()
                return
        else:
            logger.error(f"❌ Failed to get audio data. Status code: {audio_response.status_code}")
            inference_request.status = "failed"
            inference_request.error_details = f"Failed to get audio data: {audio_response.text}"
            inference_request.save()
            return
    except Exception as e:
        logger.error(f"❌ Error in process_tts_inference_request: {str(e)}")
        if 'inference_request' in locals():
            inference_request.status = "failed"
            inference_request.error_details = str(e)
            inference_request.save()
        return


@app.task
def process_video_gen_inference_request(request_id):
    """
    Process a video generation inference request.
    This task handles uploading the input image to ComfyUI and manages the video generation workflow.
    """
    try:
        inference_request = InferenceRequest.objects.get(id=request_id)
        inference_request.status = 'in_progress'
        inference_request.save()

        # Get the video generation service URL
        service_url = inference_request.video_gen_service.url.rstrip('/')
        logger.info(f"🔍 Processing video generation request {request_id} with service URL: {service_url}")

        # Initialize ComfyAPI
        comfy_api = ComfyAPI(service_url)

        # Upload the input image if one was provided
        logger.info(f"📁 Checking for input image file in request {request_id}")
        logger.info(f"📁 Input image file field: {inference_request.input_image_file}")
        logger.info(f"📁 Input image file name: {inference_request.input_image_file.name if inference_request.input_image_file else 'None'}")

        if inference_request.input_image_file:
            # Get just the filename without the path
            filename = os.path.basename(inference_request.input_image_file.name)
            logger.info(f"📁 Found input image file: {filename}")

            # Open the file in binary mode
            try:
                with inference_request.input_image_file.open('rb') as image_file:
                    files = {
                        'image': (filename, image_file, 'image/jpeg')
                    }
                    data = {
                        'type': 'input',  # This will upload to the input directory
                        'overwrite': 'true'
                    }

                    # Upload the image to ComfyUI
                    upload_url = f"{service_url}/upload/image"
                    logger.info(f"📤 Uploading image to ComfyUI at: {upload_url}")
                    response = requests.post(upload_url, files=files, data=data)

                    if response.status_code != 200:
                        error_msg = f"Failed to upload image to ComfyUI: {response.text}"
                        logger.error(f"❌ {error_msg}")
                        raise Exception(error_msg)

                    # Log the successful upload
                    upload_response = response.json()
                    logger.info(f"✅ Successfully uploaded image to ComfyUI: {upload_response}")

                    # Store the upload response in the inference request for later use
                    inference_request.response = {
                        'image_upload': upload_response
                    }
                    inference_request.save()
            except Exception as e:
                logger.error(f"❌ Error opening/uploading image file: {str(e)}")
                raise

            # Get the payload from the inference request
            payload = inference_request.payload or {}

            # Load the base workflow
            workflow = {
                "3": {
                    "inputs": {
                        "seed": payload.get('seed', 1112848028041754),
                        "steps": payload.get('steps', 20),
                        "cfg": payload.get('cfg', 6),
                        "sampler_name": "uni_pc",
                        "scheduler": "simple",
                        "denoise": 1,
                        "model": ["37", 0],
                        "positive": ["50", 0],
                        "negative": ["50", 1],
                        "latent_image": ["50", 2]
                    },
                    "class_type": "KSampler"
                },
                "6": {
                    "inputs": {
                        "text": ["57", 0],
                        "clip": ["38", 0]
                    },
                    "class_type": "CLIPTextEncode"
                },
                "7": {
                    "inputs": {
                        "text": ["59", 0],
                        "clip": ["38", 0]
                    },
                    "class_type": "CLIPTextEncode"
                },
                "8": {
                    "inputs": {
                        "samples": ["3", 0],
                        "vae": ["39", 0]
                    },
                    "class_type": "VAEDecode"
                },
                "37": {
                    "inputs": {
                        "unet_name": "wan2.1_i2v_480p_14B_fp8_e4m3fn.safetensors",
                        "weight_dtype": "default"
                    },
                    "class_type": "UNETLoader"
                },
                "38": {
                    "inputs": {
                        "clip_name": "umt5_xxl_fp8_e4m3fn_scaled.safetensors",
                        "type": "wan",
                        "device": "default"
                    },
                    "class_type": "CLIPLoader"
                },
                "39": {
                    "inputs": {
                        "vae_name": "wan_2.1_vae.safetensors"
                    },
                    "class_type": "VAELoader"
                },
                "49": {
                    "inputs": {
                        "clip_name": "clip_vision_h.safetensors"
                    },
                    "class_type": "CLIPVisionLoader"
                },
                "50": {
                    "inputs": {
                        "width": payload.get('width', 512),
                        "height": payload.get('height', 512),
                        "length": ["56", 0],
                        "batch_size": 1,
                        "positive": ["6", 0],
                        "negative": ["7", 0],
                        "vae": ["39", 0],
                        "clip_vision_output": ["51", 0],
                        "start_image": ["55", 0]
                    },
                    "class_type": "WanImageToVideo"
                },
                "51": {
                    "inputs": {
                        "crop": "none",
                        "clip_vision": ["49", 0],
                        "image": ["55", 0]
                    },
                    "class_type": "CLIPVisionEncode"
                },
                "52": {
                    "inputs": {
                        "image": filename  # Use the uploaded image filename
                    },
                    "class_type": "LoadImage"
                },
                "55": {
                    "inputs": {
                        "width": payload.get('width', 512),
                        "height": payload.get('height', 512),
                        "interpolation": "nearest",
                        "method": "stretch",
                        "condition": "always",
                        "multiple_of": 0,
                        "image": ["52", 0]
                    },
                    "class_type": "ImageResize+"
                },
                "56": {
                    "inputs": {
                        "value": payload.get('frame_length', 33)
                    },
                    "class_type": "PrimitiveInt"
                },
                "57": {
                    "inputs": {
                        "value": payload.get('positive_prompt', '')
                    },
                    "class_type": "PrimitiveStringMultiline"
                },
                "59": {
                    "inputs": {
                        "value": payload.get('negative_prompt', '')
                    },
                    "class_type": "PrimitiveStringMultiline"
                },
                "62": {
                    "inputs": {
                        "frame_rate": payload.get('fps', 16),
                        "loop_count": 0,
                        "filename_prefix": "ComfyUI",
                        "format": "video/h264-mp4",
                        "pix_fmt": "yuv420p",
                        "crf": 19,
                        "save_metadata": True,
                        "trim_to_audio": False,
                        "pingpong": False,
                        "save_output": True,
                        "images": ["8", 0]
                    },
                    "class_type": "VHS_VideoCombine"
                }
            }

            # Queue the prompt
            logger.info("📤 Queueing video generation prompt...")
            prompt_response = comfy_api.queue_prompt(workflow)
            if not prompt_response:
                raise Exception("Failed to queue prompt")

            prompt_id = prompt_response.get('prompt_id')
            logger.info(f"✅ Prompt queued with ID: {prompt_id}")

            # Wait for the prompt to complete
            logger.info("⏳ Waiting for video generation to complete...")
            result = comfy_api.wait_for_prompt_completion(prompt_id)

            if not result or 'outputs' not in result:
                raise Exception("Video generation failed or timed out")

            # Log the full result for debugging
            logger.info(f"📦 Full result from ComfyUI: {json.dumps(result, indent=2)}")

            # Get the output video
            output_data = result['outputs'].get('62', {})
            logger.info(f"📦 Output data from node 62: {json.dumps(output_data, indent=2)}")

            # The VHS_VideoCombine node stores videos in the gifs array
            if 'gifs' in output_data:
                video_data = output_data['gifs'][0]
            elif 'videos' in output_data:
                video_data = output_data['videos'][0]
            elif 'images' in output_data:
                # Some nodes might store videos in the images array
                video_data = output_data['images'][0]
            else:
                raise Exception(f"No video output found in node 62. Available keys: {list(output_data.keys())}")

            video_filename = video_data['filename']
            video_subfolder = video_data.get('subfolder', '')

            # Download the video
            logger.info(f"📥 Downloading generated video: {video_filename} from subfolder: {video_subfolder}")
            video_response = comfy_api.get_image(video_filename, video_subfolder, 'output')
            if not video_response:
                raise Exception("Failed to download generated video")

            # Save the MP4 video to the inference request
            inference_request.generated_video.save(
                f"generated_video_{inference_request.id}.mp4",
                ContentFile(video_response.content),
                save=False
            )

            # Update the response with the video generation details
            inference_request.response.update({
                'video_generation': {
                    'prompt_id': prompt_id,
                    'video_filename': video_filename,
                    'video_subfolder': video_subfolder,
                    'format': 'mp4'
                }
            })

            inference_request.status = 'completed'
            inference_request.save()
            logger.info(f"✅ Successfully generated and saved video for request {request_id}")

        else:
            logger.info("ℹ️ No input image file provided for this request")
            inference_request.status = 'failed'
            inference_request.error_details = "No input image file provided"
            inference_request.save()

    except Exception as e:
        logger.error(f"❌ Error processing video generation for InferenceRequest ID {request_id}: {str(e)}")
        if inference_request:
            inference_request.status = 'failed'
            inference_request.error_details = str(e)
            inference_request.save()
